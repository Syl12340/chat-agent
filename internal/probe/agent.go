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

	"chat-agent/internal/media"
	"chat-agent/plugins/builtin/clock"
)

const AgentID = "chat"

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
	SystemPrompt, HostPrompt, Reference     string
	Tools                                   []Tool
	DisableBuiltinClock                     bool
	RoleID, SessionID                       string
	PrepareStep                             func(context.Context, StepContext) (StepState, error)
	Guards                                  []ToolGuard
	// DecisionGate is the mandatory checkpoint for tools that declare a
	// decision requirement. Nil means no gating, which is correct when no tool
	// asks for it.
	DecisionGate DecisionGate
	// Approvals holds host-issued single-use authorizations for calls that were
	// refused only because no verdict could be obtained. Nil authorizes nothing.
	Approvals                     *ApprovalStore
	Provider                      ModelProvider
	ContextBytes, KeepRecentTurns int
	HistoryFloor                  int
	Thinking, ReasoningEffort     string
	ResolveImage                  func(context.Context, media.Image) (string, error)
	RequestID                     string
	Recorder                      RunRecorder
}

func DefaultConfig() Config {
	return Config{MaxTurns: 4, MaxToolCalls: 4, Timeout: 30 * time.Second, TokenParameter: "max_tokens", ContextBytes: 64 << 10, KeepRecentTurns: 4}
}

type toolCallKey struct{}
type toolSessionKey struct{}

// ToolCallID lets a tool scope an idempotency key to the current invocation.
func ToolCallID(ctx context.Context) string { id, _ := ctx.Value(toolCallKey{}).(string); return id }

// ToolSessionID exposes only the already-authorized opaque conversation scope
// to a tool. It is useful for issuing a same-session download receipt and does
// not grant access to the session store.
func ToolSessionID(ctx context.Context) string {
	id, _ := ctx.Value(toolSessionKey{}).(string)
	return id
}

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
	// ID is a host-owned durable identity. It is persisted in history and shown
	// to authenticated clients, but modelVisibleMessages always removes it.
	ID string `json:"id,omitempty"`
	// Source is host-only provenance for ephemeral request context.
	Source string `json:"source,omitempty"`
	// Origin marks a persisted message the user did not ask for, so the client can
	// present it differently from a reply. Empty for ordinary turns, "proactive"
	// for messages the assistant started on its own. It is persisted and shown to
	// clients, and removed for the model just like ID.
	Origin           string        `json:"origin,omitempty"`
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
	Output     string           `json:"output"`
	Error      string           `json:"error,omitempty"`
	Value      json.RawMessage  `json:"value,omitempty"`
	Code       string           `json:"code,omitempty"`
	CallID     string           `json:"call_id,omitempty"`
	DurationMS int64            `json:"duration_ms,omitempty"`
	Truncated  bool             `json:"truncated,omitempty"`
	Receipt    *BusinessReceipt `json:"receipt,omitempty"`
	// Decision is the gate trace: what was asked, what came back, and why the call
	// was allowed or refused. Absent when the tool needs no evaluation.
	Decision *DecisionRecord `json:"decision,omitempty"`
}
type Event struct {
	Type     EventType
	Text     string
	ToolCall *ToolCall
	ToolView *ToolView
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
	if c.MaxTurns < 1 || c.MaxToolCalls < 1 || c.MaxOutputTokens < 0 || c.Timeout <= 0 || len(c.HostPrompt) > 16<<10 {
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
	tools := c.Tools
	if !c.DisableBuiltinClock {
		definition := clock.Tool()
		tools = append([]Tool{{Name: definition.Name, Order: definition.Order, Usage: definition.Usage, Description: definition.Description, Parameters: definition.Parameters, Execute: func(ctx context.Context, input json.RawMessage) (ToolResult, error) {
			result, err := definition.Handler(ctx, input)
			return ToolResult{Output: result.Output}, err
		}}}, tools...)
	}
	registry, err := NewToolRegistry(tools)
	if err != nil {
		return nil, err
	}
	provider := c.Provider
	if provider == nil {
		provider = &ChatCompletionsProvider{endpoint: strings.TrimRight(c.BaseURL, "/") + "/chat/completions", key: c.APIKey, client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	}
	return &Agent{config: c, session: sess, registry: registry, executor: ToolExecutor{guards: append([]ToolGuard(nil), c.Guards...), gate: c.DecisionGate, approvals: c.Approvals}, provider: provider}, nil
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
	if a.config.Recorder != nil {
		if err := a.config.Recorder.AttemptStarted(ctx, AttemptRecord{SessionID: a.config.SessionID, RoleID: a.config.RoleID, RequestID: a.config.RequestID, HistoryFrom: len(a.session.Messages()), Inputs: cloneMessages(inputs), MaxTurns: a.config.MaxTurns, MaxTools: a.config.MaxToolCalls}); err != nil {
			return recordingError("attempt start", err)
		}
		defer func() {
			status, reason := "completed", "final_response"
			if runErr != nil {
				status, reason = "failed", "runtime_error"
			}
			if ctx.Err() != nil || errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
				status, reason = "canceled", "context_canceled"
			}
			finish := AttemptFinish{Status: status, StopReason: reason}
			if runErr != nil {
				finish.Error = runErr.Error()
			}
			if err := a.config.Recorder.AttemptFinished(context.WithoutCancel(ctx), finish); err != nil {
				runErr = errors.Join(runErr, recordingError("attempt finish", err))
			}
		}()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	a.turnStart = len(a.session.Messages())
	if err := a.session.append(inputs...); err != nil {
		return err
	}
	if a.config.ContextBytes > 0 {
		// Size the same message projection a provider receives. Display-only
		// final reasoning remains in the journal but must not force an early
		// summary when it is not sent back to the model.
		data, _ := json.Marshal(modelVisibleMessages(a.session.ContextMessagesFrom(a.config.HistoryFloor)))
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
		response, err := a.stream(ctx, turn, assembly, send)
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
			view := presentTool(assembly.Tools, call)
			send(Event{Type: EventToolCallStart, ToolCall: &call, ToolView: &view})
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
				var recordErr error
				result, recordErr = a.executor.Execute(ctx, assembly.Tools, call, a.config.Recorder, turn)
				if recordErr != nil {
					results = append(results, result)
					send(Event{Type: EventToolResult, ToolCall: &call, Result: &result})
					return recordErr
				}
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
