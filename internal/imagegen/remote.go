package imagegen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Generator is the capability the API layer needs. Both the local CLI bridge
// (Client) and the tunnelled HTTP bridge (Remote) satisfy it, so the rest of the
// system does not care where the GPU lives.
type Generator interface {
	Enabled() bool
	Profiles() []string
	DefaultProfile() string
	Generate(ctx context.Context, req Request) (*Result, error)
	// Status reports what the renderer can do right now. It is advisory: the host
	// never hides the capability because of it, and callers may still try to
	// generate. That is deliberate — a stale or partial outage should be the
	// model's judgement call, not a silently missing tool.
	Status(ctx context.Context) Status
}

// Status is a point-in-time view of the renderer, flattened so both the local CLI
// bridge and the tunnelled HTTP bridge answer the same shape.
type Status struct {
	Ready bool
	// Reason is written for a person: it explains why Ready is false.
	Reason   string
	Profiles []string
	Default  string
	// Details carries free-form diagnostics (GPU, queue, versions). It is shown to
	// the model as text and is never required by the host.
	Details map[string]any
}

// RemoteConfig points at a bridge process reachable over HTTP. The usual setup is
// an SSH reverse tunnel from the server back to the machine with the GPU, so the
// server keeps no Python, no model weights and no ComfyUI.
type RemoteConfig struct {
	BaseURL string
	// Token is optional. When set, every request carries it as a bearer token so a
	// forwarded port cannot be driven by anything else on the host.
	Token   string
	Timeout time.Duration
}

// healthTTL bounds how stale the cached capability snapshot may get. The API layer
// calls Profiles on every tool list build, which must not block on the tunnel.
const healthTTL = 20 * time.Second

type Remote struct {
	baseURL string
	token   string
	client  *http.Client

	mu       sync.Mutex
	health   remoteHealth
	checked  time.Time
	probing  bool
	lastFail string
	// known is the last profile list seen healthy, kept across outages.
	known []string
}

type remoteHealth struct {
	OK       bool     `json:"ok"`
	Comfy    bool     `json:"comfy"`
	Profiles []string `json:"profiles"`
	Default  string   `json:"default"`
	Reason   string   `json:"reason"`
	Version  string   `json:"version"`
	Bridge   string   `json:"bridge"`
	// Diagnostics are opaque here: the bridge owns their shape and we only show
	// them to the model.
	Queue  map[string]any `json:"queue"`
	GPU    map[string]any `json:"gpu"`
	Memory map[string]any `json:"memory"`
	// Specs describes the legal size range per profile.
	Specs []ProfileSpec `json:"specs"`
}

// ProfileSpec is the size envelope a profile accepts.
type ProfileSpec struct {
	Name         string  `json:"name"`
	Mode         string  `json:"mode"`
	Width        []int   `json:"width"`
	Height       []int   `json:"height"`
	Alignment    int     `json:"alignment"`
	MinFreeRAMGB float64 `json:"min_free_ram_gb"`
	Ready        bool    `json:"ready"`
}

// NewRemote returns nil when no base URL is configured, matching New's contract:
// an unconfigured capability must not exist at all.
func NewRemote(c RemoteConfig) *Remote {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		return nil
	}
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	remote := &Remote{
		baseURL: base,
		token:   strings.TrimSpace(c.Token),
		client: &http.Client{
			Timeout: timeout,
			// The tunnel endpoint is fixed; following redirects would let a
			// compromised bridge steer generation traffic elsewhere.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
	// Prime the snapshot so the first tool listing is not empty. Failure is fine:
	// the capability simply reports unavailable until a refresh succeeds.
	remote.refresh(context.Background())
	return remote
}

func (r *Remote) Enabled() bool { return r != nil }

func (r *Remote) Profiles() []string {
	if r == nil {
		return nil
	}
	r.maybeRefresh()
	r.mu.Lock()
	defer r.mu.Unlock()
	// A momentary outage must not erase which renderers exist, or the tool would
	// lose its enum and look like the capability vanished.
	out := make([]string, len(r.known))
	copy(out, r.known)
	sort.Strings(out)
	return out
}

func (r *Remote) DefaultProfile() string {
	if r == nil {
		return ""
	}
	r.maybeRefresh()
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.health.Default
}

// Healthy reports whether the bridge answered its last probe. The API layer uses
// it to tell the user "生成服务暂时离线" instead of offering a tool that fails.
func (r *Remote) Healthy() bool {
	if r == nil {
		return false
	}
	r.maybeRefresh()
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.health.OK
}

// Reason explains the last failure in words a person can act on.
func (r *Remote) Reason() string {
	if r == nil {
		return "未配置图像生成服务"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.health.OK {
		return ""
	}
	if r.health.Reason != "" {
		return r.health.Reason
	}
	return r.lastFail
}

// maybeRefresh re-probes in the background at most once per healthTTL. It never
// blocks: a slow tunnel must not stall a tool listing.
func (r *Remote) maybeRefresh() {
	r.mu.Lock()
	if r.probing || time.Since(r.checked) < healthTTL {
		r.mu.Unlock()
		return
	}
	r.probing = true
	r.mu.Unlock()

	go func() {
		r.refresh(context.Background())
	}()
}

func (r *Remote) refresh(ctx context.Context) {
	defer func() {
		r.mu.Lock()
		r.probing = false
		r.checked = time.Now()
		r.mu.Unlock()
	}()

	probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, r.baseURL+"/health", nil)
	if err != nil {
		r.setFailure("健康检查地址无效")
		return
	}
	r.authorize(request)
	response, err := r.client.Do(request)
	if err != nil {
		r.setFailure("无法连接到图像生成桥，检查隧道是否在线")
		return
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(body, &failure)
		if failure.Reason == "" {
			failure.Reason = fmt.Sprintf("图像生成桥返回 %d", response.StatusCode)
		}
		r.setFailure(failure.Reason)
		return
	}
	var health remoteHealth
	if err := json.Unmarshal(body, &health); err != nil {
		r.setFailure("图像生成桥的健康信息无法解析")
		return
	}
	if !health.OK {
		if health.Reason == "" {
			health.Reason = "图像生成桥未就绪"
		}
		health.OK = false
	}
	r.mu.Lock()
	r.health = health
	r.lastFail = ""
	if len(health.Profiles) > 0 {
		r.known = append([]string(nil), health.Profiles...)
	}
	r.mu.Unlock()
}

func (r *Remote) setFailure(reason string) {
	r.mu.Lock()
	r.health = remoteHealth{}
	r.lastFail = reason
	r.mu.Unlock()
}

func (r *Remote) authorize(request *http.Request) {
	if r.token != "" {
		request.Header.Set("Authorization", "Bearer "+r.token)
	}
	request.Header.Set("Accept", "application/json")
}

// remoteGeneration is the bridge's reply to POST /generate.
type remoteGeneration struct {
	JobID   string `json:"job_id"`
	Profile string `json:"profile"`
	Seed    int64  `json:"seed"`
	Note    string `json:"note"`
	Images  []struct {
		MIME string `json:"mime"`
		Data string `json:"base64"`
	} `json:"images"`
	Error       string `json:"error"`
	Unavailable bool   `json:"unavailable"`
}

// Generate forwards one request. Errors are classified the same way the local
// bridge classifies them, so the tool layer keeps a single error vocabulary.
func (r *Remote) Generate(ctx context.Context, req Request) (*Result, error) {
	if r == nil {
		return nil, ErrUnavailable
	}
	payload := map[string]any{
		"prompt":          req.Prompt,
		"negative":        req.Negative,
		"profile":         req.Profile,
		"width":           req.Width,
		"height":          req.Height,
		"steps":           req.Steps,
		"seed":            req.Seed,
		"cfg":             req.CFG,
		"min_free_ram_gb": req.MinFreeRAMGB,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.New("请求无法编码")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/generate", strings.NewReader(string(body)))
	if err != nil {
		return nil, ErrUnavailable
	}
	r.authorize(httpReq)
	httpReq.Header.Set("Content-Type", "application/json")

	response, err := r.client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrUnavailable
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return nil, ErrUnavailable
	}
	var reply remoteGeneration
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, errors.New("图像生成桥返回了无法解析的结果")
	}
	if reply.Error != "" {
		if reply.Unavailable || response.StatusCode == http.StatusServiceUnavailable {
			return nil, fmt.Errorf("%w：%s", ErrUnavailable, reply.Error)
		}
		return nil, errors.New(reply.Error)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w：桥返回 %d", ErrUnavailable, response.StatusCode)
	}
	return decodeRemoteImages(reply)
}

func decodeRemoteImages(reply remoteGeneration) (*Result, error) {
	result := &Result{JobID: reply.JobID, Profile: reply.Profile, Seed: reply.Seed}
	for _, image := range reply.Images {
		data, err := base64.StdEncoding.DecodeString(image.Data)
		if err != nil || len(data) == 0 {
			continue
		}
		result.Images = append(result.Images, Image{Data: data, MIME: image.MIME})
	}
	if len(result.Images) == 0 {
		return nil, errors.New("图像生成桥没有返回图片")
	}
	return result, nil
}

// Status reports the cached health snapshot, refreshing it if it is stale. It
// never blocks longer than the probe timeout, so a dead tunnel cannot stall a
// tool listing.
func (r *Remote) Status(ctx context.Context) Status {
	if r == nil {
		return Status{Reason: "未配置图像生成服务"}
	}
	r.refreshIfStale(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	status := Status{Ready: r.health.OK, Reason: r.health.Reason, Default: r.health.Default}
	if status.Reason == "" && !status.Ready {
		status.Reason = r.lastFail
	}
	// Keep the last known profile list even while unhealthy: a brief outage must
	// not erase which renderers exist.
	status.Profiles = append(status.Profiles, r.known...)
	if r.health.OK {
		status.Details = map[string]any{
			"bridge": r.health.Bridge,
			"comfy":  r.health.Comfy,
			"queue":  r.health.Queue,
			"gpu":    r.health.GPU,
			"memory": r.health.Memory,
			"specs":  r.health.Specs,
		}
	}
	return status
}

// refreshIfStale probes when the snapshot is older than healthTTL, waiting for the
// result because a caller asking for status wants the answer now, not a promise.
func (r *Remote) refreshIfStale(ctx context.Context) {
	r.mu.Lock()
	fresh := time.Since(r.checked) < healthTTL
	probing := r.probing
	r.mu.Unlock()
	if fresh || probing {
		return
	}
	r.refresh(ctx)
}
