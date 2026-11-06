// Package probe implements chat-agent's minimal runtime with the Go standard library.
package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"pocket-agent/internal/media"
)

const AgentID = "pocket"

type Config struct {
	BaseURL, APIKey, Model string
	// UtilityModel handles background work that never faces the user: history
	// summaries. Empty means Model. Compaction is the one place a cheaper model
	// is safe, because its output is validated and rejected on failure.
	UtilityModel                            string
	MaxTurns, MaxToolCalls, MaxOutputTokens int
	Timeout                                 time.Duration
	TokenParameter                          string
	Demo                                    bool
	SystemPrompt, Reference                 string
	Tools                                   []Tool
	RoleID, SessionID                       string
	PrepareStep                             func(context.Context, StepContext) (StepState, error)
	Guards                                  []ToolGuard
	Provider                                ModelProvider
	ContextBytes, KeepRecentTurns           int
	HistoryFloor                            int
	Thinking, ReasoningEffort               string
	ResolveImage                            func(context.Context, media.Image) (string, error)
}

func DefaultConfig() Config {
	return Config{MaxTurns: 4, MaxToolCalls: 4, Timeout: 30 * time.Second, TokenParameter: "max_tokens", ContextBytes: 64 << 10, KeepRecentTurns: 4}
}

type toolCallKey struct{}

// ToolCallID lets a tool scope an idempotency key to the current invocation.
func ToolCallID(ctx context.Context) string { id, _ := ctx.Value(toolCallKey{}).(string); return id }

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}
type Message struct {
	// Source is host-only provenance for ephemeral request context.
	Source           string        `json:"-"`
	Role             string        `json:"role"`
	Content          string        `json:"content"`
	ToolCalls        []ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID       string        `json:"tool_call_id,omitempty"`
	Images           []media.Image `json:"images,omitempty"`
	ReasoningContent string        `json:"reasoning_content,omitempty"`
}
type EventType string

const (
	EventTextDelta       EventType = "text_delta"
	EventToolCallStart   EventType = "tool_start"
	EventToolResult      EventType = "tool_result"
	EventDone            EventType = "done"
	EventError           EventType = "error"
	EventCompactionStart EventType = "compaction_start"
	EventCompactionDone  EventType = "compaction_done"
	EventThinking        EventType = "thinking"
	EventReasoningDelta  EventType = "reasoning_delta"
)

type ToolResult struct {
	Output     string          `json:"output"`
	Error      string          `json:"error,omitempty"`
	Value      json.RawMessage `json:"value,omitempty"`
	Code       string          `json:"code,omitempty"`
	CallID     string          `json:"call_id,omitempty"`
	DurationMS int64           `json:"duration_ms,omitempty"`
	Truncated  bool            `json:"truncated,omitempty"`
}
type Event struct {
	Type     EventType
	Text     string
	ToolCall *ToolCall
	Result   *ToolResult
	Error    error
}
type Agent struct {
	config    Config
	session   *Session
	registry  *ToolRegistry
	executor  ToolExecutor
	provider  ModelProvider
	turnStart int
}

func New(c Config, sess *Session) (*Agent, error) {
	if !c.Demo && c.Provider == nil {
		u, err := url.Parse(c.BaseURL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("base URL must be HTTP(S) without credentials, query or fragment")
		}
	}
	if (!c.Demo && (strings.TrimSpace(c.Model) == "" || (c.Provider == nil && c.APIKey == ""))) || sess == nil {
		return nil, errors.New("API key, model and session are required")
	}
	if c.MaxTurns < 1 || c.MaxToolCalls < 1 || c.MaxOutputTokens < 0 || c.Timeout <= 0 {
		return nil, errors.New("turn, tool and timeout limits must be positive; output tokens cannot be negative (0 uses provider default)")
	}
	if c.ContextBytes < 0 || c.KeepRecentTurns < 0 {
		return nil, errors.New("context budget and preserved turns cannot be negative")
	}
	if c.TokenParameter != "max_tokens" && c.TokenParameter != "max_completion_tokens" {
		return nil, errors.New("token parameter must be max_tokens or max_completion_tokens")
	}
	if c.Thinking != "" && c.Thinking != "enabled" && c.Thinking != "disabled" {
		return nil, errors.New("invalid thinking setting")
	}
	if c.ReasoningEffort != "" && (c.Thinking != "enabled" || !validEffort(c.ReasoningEffort)) {
		return nil, errors.New("invalid reasoning effort")
	}
	registry, err := NewToolRegistry(append([]Tool{clock()}, c.Tools...))
	if err != nil {
		return nil, err
	}
	provider := c.Provider
	if provider == nil {
		provider = &ChatCompletionsProvider{endpoint: strings.TrimRight(c.BaseURL, "/") + "/chat/completions", key: c.APIKey, client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	}
	return &Agent{config: c, session: sess, registry: registry, executor: ToolExecutor{guards: append([]ToolGuard(nil), c.Guards...)}, provider: provider}, nil
}

func clock() Tool {
	return Tool{Name: "get_current_time", Order: 100, Usage: "call it whenever the answer depends on the real current time instead of guessing.", Description: "Get current UTC server time in RFC3339 format.", Parameters: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`), Execute: func(ctx context.Context, input json.RawMessage) (ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return ToolResult{}, err
		}
		var args map[string]json.RawMessage
		if err := json.Unmarshal(input, &args); err != nil || args == nil || len(args) != 0 {
			return ToolResult{}, errors.New("clock arguments must be an empty object")
		}
		return ToolResult{Output: time.Now().UTC().Format(time.RFC3339)}, nil
	}}
}

// Run is synchronous; the response body is closed before returning. Callbacks
// must return promptly. One session admits only one run at a time.
func Run(ctx context.Context, a *Agent, timeout time.Duration, prompt string, emit func(Event)) error {
	return RunMessages(ctx, a, timeout, []Message{{Role: "user", Content: prompt}}, emit)
}

// RunMessages preserves individual user message boundaries in a merged turn.
func RunMessages(ctx context.Context, a *Agent, timeout time.Duration, inputs []Message, emit func(Event)) (runErr error) {
	if timeout <= 0 || len(inputs) == 0 || len(inputs) > 8 {
		return errors.New("invalid user message batch")
	}
	size, imageCount := 0, 0
	for _, input := range inputs {
		if input.Role != "user" || input.Source != "" || strings.TrimSpace(input.Content) == "" && len(input.Images) == 0 || len(input.ToolCalls) > 0 || input.ToolCallID != "" || input.ReasoningContent != "" {
			return errors.New("invalid user message")
		}
		if err := validateImages(input.Images); err != nil {
			return err
		}
		imageCount += len(input.Images)
		size += len(input.Content)
	}
	if size > 64<<10 || imageCount > 3 {
		return errors.New("prompt exceeds 64 KiB limit")
	}
	if !a.session.mu.TryLock() {
		return errors.New("session is busy")
	}
	defer a.session.mu.Unlock()
	if a.session.closed {
		return errors.New("session is closed")
	}
	if timeout > a.config.Timeout {
		timeout = a.config.Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	send := func(e Event) {
		if emit != nil {
			emit(e)
		}
	}
	defer func() {
		if ctx.Err() != nil {
			runErr = errors.Join(context.Cause(ctx), runErr)
		}
		if runErr != nil {
			send(Event{Type: EventError, Error: runErr})
		} else {
			send(Event{Type: EventDone})
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	a.turnStart = len(a.session.Messages())
	if err := a.session.append(inputs...); err != nil {
		return err
	}
	if a.config.ContextBytes > 0 {
		data, _ := json.Marshal(a.session.ContextMessagesFrom(a.config.HistoryFloor))
		if len(data) > a.config.ContextBytes {
			send(Event{Type: EventCompactionStart})
			changed, err := a.compactLocked(ctx)
			if err != nil {
				return err
			}
			text := ""
			if !changed {
				text = "skipped"
			}
			send(Event{Type: EventCompactionDone, Text: text})
		}
	}
	calls := 0
	lastBatch := ""
	lastResults := []ToolResult{}
	forceFinal := false
	for turn := 0; turn < a.config.MaxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		toolsAllowed := !forceFinal && turn < a.config.MaxTurns-1 && calls < a.config.MaxToolCalls
		assembly, err := a.assemble(ctx, a.stepContext(turn, calls, toolsAllowed))
		if err != nil {
			return err
		}
		response, err := a.stream(ctx, assembly, send)
		if err != nil {
			return err
		}
		if len(response.ToolCalls) == 0 {
			return a.session.append(response)
		}
		if !toolsAllowed {
			return errors.New("model requested tools after the final-response boundary")
		}
		// Commit complete assistant/tool batches. Incomplete streams execute nothing.
		batch := []Message{response}
		signatures := []string{}
		for _, call := range response.ToolCalls {
			args, decodeErr := decodeArguments([]byte(call.Function.Arguments))
			canonical := []byte(call.Function.Arguments)
			if decodeErr == nil {
				canonical, _ = json.Marshal(args)
			}
			signatures = append(signatures, call.Function.Name+"\x00"+string(canonical))
		}
		encodedBatch, _ := json.Marshal(signatures)
		repeated := string(encodedBatch) == lastBatch && len(lastResults) == len(response.ToolCalls)
		results := []ToolResult{}
		for i := range response.ToolCalls {
			call := response.ToolCalls[i]
			send(Event{Type: EventToolCallStart, ToolCall: &call})
			result := ToolResult{}
			if ctx.Err() != nil {
				result = normalizeToolResult(toolFailure("TOOL_CANCELED", "tool canceled before dispatch"))
			} else if _, admitted := assembly.Tools.byName[call.Function.Name]; !admitted {
				result = normalizeToolResult(toolFailure("UNKNOWN_TOOL", "tool is not explicitly allowed for this step"))
			} else if repeated {
				result = lastResults[i]
				result.CallID = call.ID
				forceFinal = true
			} else if calls >= a.config.MaxToolCalls {
				result = normalizeToolResult(toolFailure("TOOL_LIMIT", "tool invocation limit reached"))
			} else {
				calls++
				result = a.executor.Execute(ctx, assembly.Tools, call)
			}
			results = append(results, result)
			batch = append(batch, Message{Role: "tool", Content: result.modelContent(), ToolCallID: call.ID})
			send(Event{Type: EventToolResult, ToolCall: &call, Result: &result})
		}
		if err := a.session.append(batch...); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		lastBatch, lastResults = string(encodedBatch), results
	}
	return fmt.Errorf("agent exceeded maximum turns (%d)", a.config.MaxTurns)
}
