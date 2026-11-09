// Package imagegen gives the agent one image-generation capability, backed by the
// local image agent bridge (E:\IAST\image-generation\agent-bridge).
//
// The bridge is deliberately the only entry point: it holds an approved profile
// list, converts those canvas workflows to ComfyUI API prompts itself, rejects
// concurrent jobs, and enforces RAM and dimension limits. The agent never talks
// to ComfyUI directly and never supplies a workflow, so "which model family" is
// a configuration decision rather than something a model can choose.
package imagegen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Config struct {
	// Python is the interpreter that runs the bridge CLI.
	Python string
	// Script is the bridge's imgctl.py path.
	Script string
	// Profiles is the allow-list this agent may request.
	Profiles []string
	// AllowAllProfiles lets a trusted local bridge expose whatever the renderer
	// reports instead of a fixed list. It is for the on-device bridge only; the
	// server keeps an explicit allow-list so a model cannot name an arbitrary
	// workflow file.
	AllowAllProfiles bool
	// DefaultProfile is used when a request names none.
	DefaultProfile string
	Timeout        time.Duration
	PollInterval   time.Duration
	MaxBytes       int64
}

type Request struct {
	Profile  string
	Prompt   string
	Negative string
	Width    int
	Height   int
	Steps    int
	Seed     int64
	CFG      float64
	// MinFreeRAMGB overrides the profile's host-memory floor for one call. Zero
	// means "use the profile value". The renderer clamps it to its own hard
	// minimum, so a small number weakens the check rather than disabling it.
	MinFreeRAMGB float64
}

type Image struct {
	Data []byte
	MIME string
	Name string
}

type Result struct {
	JobID   string
	Profile string
	Seed    int64
	Images  []Image
}

// ErrUnavailable means the bridge itself is not answering. It is separate from a
// rejected request so callers can say "try later" instead of "your prompt failed".
var ErrUnavailable = errors.New("图像生成服务暂不可用")

// runner executes the bridge CLI and returns its raw stdout. It exists so tests
// can exercise the protocol without a real interpreter.
type runner func(ctx context.Context, args []string) ([]byte, error)

type Client struct {
	python   string
	script   string
	run      runner
	profiles map[string]bool
	allowAll bool
	fallback string
	timeout  time.Duration
	poll     time.Duration
	maxBytes int64

	mu       sync.Mutex
	closed   bool
}

const (
	maxPromptChars = 1500
	maxNegative    = 1000
	minSide        = 256
	maxSide        = 1536
	maxSteps       = 40
	// bridgeTimeout bounds the whole submit+poll+download sequence.
	bridgeTimeout = 4 * time.Minute
)

func New(c Config) *Client {
	python := strings.TrimSpace(c.Python)
	script := strings.TrimSpace(c.Script)
	if python == "" || script == "" {
		return nil
	}
	if _, err := os.Stat(python); err != nil {
		return nil
	}
	if info, err := os.Stat(script); err != nil || info.IsDir() {
		return nil
	}
	allowed := map[string]bool{}
	for _, name := range c.Profiles {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			allowed[trimmed] = true
		}
	}
	fallback := strings.TrimSpace(c.DefaultProfile)
	if len(allowed) == 0 && !c.AllowAllProfiles {
		return nil
	}
	if len(allowed) > 0 && (fallback == "" || !allowed[fallback]) {
		// Pick a deterministic default so behaviour never depends on map order.
		names := make([]string, 0, len(allowed))
		for name := range allowed {
			names = append(names, name)
		}
		sortStrings(names)
		fallback = names[0]
	}
	timeout := c.Timeout
	if timeout <= 0 || timeout > bridgeTimeout {
		timeout = bridgeTimeout
	}
	poll := c.PollInterval
	if poll <= 0 || poll > 5*time.Second {
		poll = 2 * time.Second
	}
	maxBytes := c.MaxBytes
	if maxBytes <= 0 || maxBytes > 32<<20 {
		maxBytes = 16 << 20
	}
	client := &Client{
		python:   python,
		script:   script,
		profiles: allowed,
		allowAll: c.AllowAllProfiles && len(allowed) == 0,
		fallback: fallback,
		timeout:  timeout,
		poll:     poll,
		maxBytes: maxBytes,
	}
	client.run = client.execBridge
	return client
}

func (c *Client) Enabled() bool { return c != nil }

// Profiles returns the allow-list so a tool description can name real options.
func (c *Client) Profiles() []string {
	if c == nil {
		return nil
	}
	names := make([]string, 0, len(c.profiles))
	for name := range c.profiles {
		names = append(names, name)
	}
	sortStrings(names)
	return names
}

func (c *Client) DefaultProfile() string {
	if c == nil {
		return ""
	}
	return c.fallback
}

func (c *Client) validate(req Request) (string, error) {
	profile := strings.TrimSpace(req.Profile)
	if profile == "" {
		profile = c.fallback
	}
	if !c.allowAll && !c.profiles[profile] {
		return "", fmt.Errorf("不可用的图像档案：%s", profile)
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return "", errors.New("缺少画面描述")
	}
	if len(req.Prompt) > maxPromptChars {
		return "", fmt.Errorf("画面描述不能超过 %d 个字符", maxPromptChars)
	}
	if len(req.Negative) > maxNegative {
		return "", errors.New("反向描述过长")
	}
	for name, side := range map[string]int{"宽": req.Width, "高": req.Height} {
		if side != 0 && (side < minSide || side > maxSide) {
			return "", fmt.Errorf("%s必须在 %d-%d 之间", name, minSide, maxSide)
		}
	}
	if req.Steps < 0 || req.Steps > maxSteps {
		return "", fmt.Errorf("步数必须在 1-%d 之间", maxSteps)
	}
	if req.CFG < 0 || req.CFG > 30 {
		return "", errors.New("CFG 超出范围")
	}
	return profile, nil
}

type submitResult struct {
	JobID   string `json:"job_id"`
	Status  string `json:"status"`
	Profile string `json:"profile"`
	Seed    int64  `json:"seed"`
	OK      *bool  `json:"ok"`
	Message string `json:"message"`
	Error   string `json:"error"`
}

type statusResult struct {
	JobID     string `json:"job_id"`
	State     string `json:"state"`
	Completed bool   `json:"completed"`
}

type resultResult struct {
	JobID   string `json:"job_id"`
	Ready   bool   `json:"ready"`
	Profile string `json:"profile"`
	Seed    int64  `json:"seed"`
	Images  []struct {
		Path     string `json:"path"`
		Filename string `json:"filename"`
	} `json:"images"`
}

// Generate renders one picture through the bridge and returns its bytes.
func (c *Client) Generate(ctx context.Context, req Request) (*Result, error) {
	if c == nil {
		return nil, errors.New("图像生成未配置")
	}
	profile, err := c.validate(req)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	args := []string{"submit", "--profile", profile, "--prompt", req.Prompt}
	if req.Negative != "" {
		args = append(args, "--negative-prompt", req.Negative)
	}
	if req.Width > 0 {
		args = append(args, "--width", itoa(req.Width))
	}
	if req.Height > 0 {
		args = append(args, "--height", itoa(req.Height))
	}
	if req.Steps > 0 {
		args = append(args, "--steps", itoa(req.Steps))
	}
	if req.Seed != 0 {
		args = append(args, "--seed", i64toa(req.Seed))
	}
	if req.CFG > 0 {
		args = append(args, "--cfg", ftoa(req.CFG))
	}
	if req.MinFreeRAMGB > 0 {
		args = append(args, "--min-free-ram-gb", ftoa(req.MinFreeRAMGB))
	}

	var submitted submitResult
	if err := c.call(ctx, args, &submitted); err != nil {
		return nil, err
	}
	if submitted.JobID == "" {
		return nil, errors.New("图像服务没有返回作业号")
	}

	if err := c.wait(ctx, submitted.JobID); err != nil {
		return nil, err
	}

	var finished resultResult
	if err := c.call(ctx, []string{"result", submitted.JobID}, &finished); err != nil {
		return nil, err
	}
	if !finished.Ready {
		return nil, errors.New("图像作业尚未完成")
	}
	out := &Result{JobID: submitted.JobID, Profile: finished.Profile, Seed: finished.Seed}
	if out.Profile == "" {
		out.Profile = profile
	}
	for _, item := range finished.Images {
		data, mime, err := c.readImage(item.Path)
		if err != nil {
			return nil, err
		}
		out.Images = append(out.Images, Image{Data: data, MIME: mime, Name: item.Filename})
	}
	if len(out.Images) == 0 {
		return nil, errors.New("图像作业没有产出图片")
	}
	return out, nil
}

func (c *Client) wait(ctx context.Context, jobID string) error {
	for {
		var status statusResult
		if err := c.call(ctx, []string{"status", jobID}, &status); err != nil {
			return err
		}
		switch status.State {
		case "success", "succeeded":
			return nil
		case "failed", "error", "cancelled":
			return errors.New("图像生成失败")
		}
		if status.Completed {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("图像生成超时")
		case <-time.After(c.poll):
		}
	}
}

// call runs one bridge command and decodes its JSON stdout. The bridge prints a
// JSON object even when it fails, so the message reaches the user intact.
func (c *Client) call(ctx context.Context, args []string, out any) error {
	stdout, runErr := c.run(ctx, args)
	payload := extractJSON(stdout)
	if payload == nil {
		if ctx.Err() != nil {
			return errors.New("图像生成超时")
		}
		return ErrUnavailable
	}
	var envelope struct {
		OK      *bool  `json:"ok"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	_ = json.Unmarshal(payload, &envelope)
	if envelope.OK != nil && !*envelope.OK {
		if message := strings.TrimSpace(envelope.Message); message != "" {
			return errors.New(message)
		}
		return errors.New("图像请求被拒绝")
	}
	if runErr != nil && envelope.OK == nil {
		// Non-zero exit without an explicit failure envelope is a bridge fault.
		return ErrUnavailable
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return errors.New("图像服务响应无法解析")
	}
	return nil
}

// execBridge is the production runner: it shells out to the bridge CLI.
func (c *Client) execBridge(ctx context.Context, args []string) ([]byte, error) {
	full := append([]string{c.script}, args...)
	cmd := exec.CommandContext(ctx, c.python, full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Dir = filepath.Dir(c.script)
	if err := cmd.Run(); err != nil {
		// stdout still carries the bridge's JSON error envelope.
		if len(stdout.Bytes()) > 0 {
			return stdout.Bytes(), err
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

func (c *Client) readImage(path string) ([]byte, string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, "", errors.New("图像路径为空")
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return nil, "", errors.New("图像文件不存在")
	}
	if info.Size() > c.maxBytes {
		return nil, "", errors.New("图像文件过大")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", errors.New("图像读取失败")
	}
	mime := sniffImage(data)
	if mime == "" {
		return nil, "", errors.New("产物不是图片")
	}
	return data, mime, nil
}

func sniffImage(data []byte) string {
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp"
	}
	return ""
}

// extractJSON finds the outermost JSON object in a stream that may also carry
// PowerShell or Python noise.
func extractJSON(raw []byte) []byte {
	start := bytes.IndexByte(raw, '{')
	end := bytes.LastIndexByte(raw, '}')
	if start < 0 || end <= start {
		return nil
	}
	candidate := raw[start : end+1]
	if !json.Valid(candidate) {
		return nil
	}
	return candidate
}

func sortStrings(items []string) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j] < items[j-1]; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

func itoa(value int) string     { return fmt.Sprintf("%d", value) }
func i64toa(value int64) string { return fmt.Sprintf("%d", value) }
func ftoa(value float64) string { return fmt.Sprintf("%g", value) }
