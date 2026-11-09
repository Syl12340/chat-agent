// Package decision wraps the platform's structured decision endpoint
// (ecnu-decision, SystemOne protocol). It answers classification, yes/no and
// ordered-scoring questions with probability distributions instead of free
// text, so callers never parse model prose.
//
// The service is optional: New returns nil when it is not configured, and every
// call fails soft so a companion turn never breaks because a side model is down.
package decision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	Timeout time.Duration
}

// Question types accepted by the endpoint. "noul" is the endpoint's own spelling
// for a boolean question; sending "bool" is rejected upstream.
const (
	TypeChoice = "choice"
	TypeNoul   = "noul"
	TypeScore  = "score"
)

// Limits mirror the upstream contract, tightened to what a single chat turn
// should ever need.
const (
	maxQuestions    = 8
	maxStateChars   = 8000
	maxImages       = 4
	maxChoiceLabels = 40
	maxScoreBands   = 10
	minScoreBands   = 2
	maxInstruction  = 1024
)

type Client struct {
	endpoint string
	key      string
	model    string
	http     *http.Client

	mu       sync.Mutex
	failures int
	openTill time.Time
}

// breakerCooldown keeps an unavailable service from costing every turn a
// timeout; the breaker is short because decisions are cheap when healthy.
const breakerCooldown = 60 * time.Second

const breakerThreshold = 2

// Question is one判定 request. Criteria carries the label shape for the type:
// choice -> map[labelID]description, score -> ordered []string, noul -> optional
// map[string]string with exactly "true"/"false".
type Question struct {
	Type         string
	Instructions string
	Criteria     any
}

type Request struct {
	// State is the material under judgement: a string or any JSON object.
	State     any
	Images    []string
	Questions map[string]Question
}

// Answer is the per-question result. Which fields are populated depends on Type.
type Answer struct {
	Type          string
	Choice        string
	Labels        map[string]float64 // choice probabilities
	Noul          float64            // probability the statement holds
	Score         float64            // zero-based expected band
	Bands         map[string]float64 // score probabilities keyed by band index
	Legend        map[string]string  // band index -> original wording
	Confidence    float64
	LabelMass     float64
	ArgmaxIsLabel bool
}

type Result struct {
	ID       string
	Model    string
	Answers  map[string]Answer
	InputTok int
	Latency  time.Duration
	Warnings []string
}

func New(c Config) *Client {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" || c.APIKey == "" || strings.TrimSpace(c.Model) == "" {
		return nil
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" {
		return nil
	}
	timeout := c.Timeout
	if timeout <= 0 || timeout > 30*time.Second {
		timeout = 12 * time.Second
	}
	return &Client{
		endpoint: base + "/systemone",
		key:      c.APIKey,
		model:    strings.TrimSpace(c.Model),
		http:     &http.Client{Timeout: timeout},
	}
}

func (c *Client) Enabled() bool { return c != nil }

func (c *Client) allow() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.openTill.IsZero() || time.Now().After(c.openTill) {
		return true
	}
	return false
}

func (c *Client) record(ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ok {
		c.failures = 0
		c.openTill = time.Time{}
		return
	}
	c.failures++
	if c.failures >= breakerThreshold {
		c.openTill = time.Now().Add(breakerCooldown)
		c.failures = 0
	}
}

// Validate rejects a request the endpoint would refuse anyway, so the model gets
// a clear error instead of a 422 payload.
func (r Request) Validate() error {
	if r.State == nil {
		return errors.New("缺少待判断的材料")
	}
	if text, ok := r.State.(string); ok && strings.TrimSpace(text) == "" {
		return errors.New("待判断的材料不能为空")
	}
	if len(r.Questions) == 0 {
		return errors.New("至少需要一道问题")
	}
	if len(r.Questions) > maxQuestions {
		return fmt.Errorf("一次最多 %d 道问题", maxQuestions)
	}
	if len(r.Images) > maxImages {
		return fmt.Errorf("一次最多 %d 张图片", maxImages)
	}
	for _, raw := range r.Images {
		if !strings.HasPrefix(raw, "data:image/png;base64,") &&
			!strings.HasPrefix(raw, "data:image/jpeg;base64,") &&
			!strings.HasPrefix(raw, "data:image/jpg;base64,") &&
			!strings.HasPrefix(raw, "data:image/webp;base64,") {
			return errors.New("图片只支持 png / jpeg / webp 的 base64 Data URI")
		}
	}
	for id, q := range r.Questions {
		if strings.TrimSpace(id) == "" || len(id) > 32 {
			return errors.New("问题 ID 必须为 1-32 个字符")
		}
		if strings.TrimSpace(q.Instructions) == "" {
			return fmt.Errorf("问题 %s 缺少判断要求", id)
		}
		if len(q.Instructions) > maxInstruction {
			return fmt.Errorf("问题 %s 的判断要求过长", id)
		}
		switch q.Type {
		case TypeChoice:
			labels, ok := q.Criteria.(map[string]string)
			if !ok || len(labels) == 0 {
				return fmt.Errorf("问题 %s 的分类必须是非空对象", id)
			}
			if len(labels) > maxChoiceLabels {
				return fmt.Errorf("问题 %s 的标签过多", id)
			}
			for label, desc := range labels {
				if strings.TrimSpace(label) == "" || strings.TrimSpace(desc) == "" {
					return fmt.Errorf("问题 %s 的标签和描述都不能为空", id)
				}
			}
		case TypeScore:
			bands, ok := q.Criteria.([]string)
			if !ok || len(bands) < minScoreBands || len(bands) > maxScoreBands {
				return fmt.Errorf("问题 %s 的评分需要 2-%d 个有序档位", id, maxScoreBands)
			}
			for _, band := range bands {
				if strings.TrimSpace(band) == "" {
					return fmt.Errorf("问题 %s 的档位描述不能为空", id)
				}
			}
		case TypeNoul:
			if q.Criteria == nil {
				continue
			}
			table, ok := q.Criteria.(map[string]string)
			if !ok {
				return fmt.Errorf("问题 %s 的 true/false 口径必须是对象", id)
			}
			if _, hasTrue := table["true"]; !hasTrue || len(table) != 2 {
				return fmt.Errorf("问题 %s 的 true/false 口径必须恰好包含 true 和 false", id)
			}
		default:
			return fmt.Errorf("问题 %s 的类型必须是 choice、noul 或 score", id)
		}
	}
	return nil
}

type wireQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

type wireRequest struct {
	Model     string                  `json:"model"`
	State     any                     `json:"state"`
	Questions map[string]wireQuestion `json:"questions"`
	Images    []string                `json:"images,omitempty"`
}

type wireAnswer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice"`
	Noul          float64            `json:"noul"`
	Score         float64            `json:"score"`
	Legend        map[string]string  `json:"legend"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
}

type wireResponse struct {
	ID      string                `json:"id"`
	Model   string                `json:"model"`
	Answers map[string]wireAnswer `json:"answers"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Diagnostics map[string]struct {
		LabelMass     float64 `json:"label_mass"`
		ArgmaxIsLabel bool    `json:"argmax_is_label"`
	} `json:"diagnostics"`
	Gateway struct {
		RequestedModel  string   `json:"requested_model"`
		UpstreamLatency float64  `json:"upstream_latency_ms"`
		Warnings        []string `json:"warnings"`
	} `json:"gateway"`
	Detail json.RawMessage `json:"detail"`
	Error  struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Decide submits one judgement batch and returns per-question answers.
func (c *Client) Decide(ctx context.Context, req Request) (*Result, error) {
	if c == nil {
		return nil, errors.New("决策模型未配置")
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if !c.allow() {
		return nil, errors.New("决策模型暂不可用")
	}
	questions := make(map[string]wireQuestion, len(req.Questions))
	for id, q := range req.Questions {
		questions[id] = wireQuestion{Type: q.Type, Instructions: q.Instructions, Criteria: q.Criteria}
	}
	payload, err := json.Marshal(wireRequest{Model: c.model, State: req.State, Questions: questions, Images: req.Images})
	if err != nil {
		return nil, errors.New("决策请求无法编码")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("决策请求创建失败")
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.key)

	start := time.Now()
	resp, err := c.http.Do(httpReq)
	if err != nil {
		c.record(false)
		return nil, errors.New("决策模型连接失败")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		c.record(false)
		return nil, errors.New("决策模型响应读取失败")
	}
	elapsed := time.Since(start)
	if resp.StatusCode != http.StatusOK {
		// A 4xx means the service is healthy and rejected this request; only 5xx
		// counts as an outage for the breaker.
		c.record(resp.StatusCode < http.StatusInternalServerError)
		return nil, fmt.Errorf("决策模型返回 %d：%s", resp.StatusCode, upstreamMessage(body))
	}
	var parsed wireResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		c.record(false)
		return nil, errors.New("决策模型响应无法解析")
	}
	c.record(true)

	result := &Result{
		ID:       parsed.ID,
		Model:    parsed.Gateway.RequestedModel,
		Answers:  make(map[string]Answer, len(parsed.Answers)),
		InputTok: parsed.Usage.InputTokens,
		Latency:  elapsed,
		Warnings: parsed.Gateway.Warnings,
	}
	for id, ans := range parsed.Answers {
		out := Answer{Type: ans.Type, Choice: ans.Choice, Confidence: ans.Confidence}
		if diag, ok := parsed.Diagnostics[id]; ok {
			out.LabelMass = diag.LabelMass
			out.ArgmaxIsLabel = diag.ArgmaxIsLabel
		}
		switch ans.Type {
		case TypeChoice:
			out.Labels = ans.Probabilities
		case TypeNoul:
			out.Noul = ans.Noul
		case TypeScore:
			out.Score = ans.Score
			out.Bands = ans.Probabilities
			out.Legend = ans.Legend
		}
		result.Answers[id] = out
	}
	return result, nil
}

// upstreamMessage prefers error.message and falls back to a parsed detail array,
// because the gateway uses two different error shapes.
func upstreamMessage(body []byte) string {
	var parsed struct {
		Detail json.RawMessage `json:"detail"`
		Error  struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "无法识别的错误响应"
	}
	if msg := strings.TrimSpace(parsed.Error.Message); msg != "" {
		return msg
	}
	if len(parsed.Detail) > 0 {
		var items []struct {
			Msg string `json:"msg"`
		}
		if json.Unmarshal(parsed.Detail, &items) == nil && len(items) > 0 {
			return items[0].Msg
		}
		var text string
		if json.Unmarshal(parsed.Detail, &text) == nil && text != "" {
			return text
		}
	}
	return "请求被拒绝"
}
