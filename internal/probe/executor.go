package probe

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"
)

// ToolExecutor borrows DSH's guarded pipeline design (MIT), not its runtime.
// It is serial, never retries, and never abandons a running tool goroutine.
// Business handlers must forward cancellation and keep their own atomic guards.
type ToolExecutor struct{ guards []ToolGuard }

func (e *ToolExecutor) Execute(ctx context.Context, set *ToolSet, call ToolCall) (result ToolResult) {
	started := time.Now()
	defer func() {
		if recover() != nil {
			result = toolFailure("TOOL_INTERNAL", "tool execution failed; inspect saved state before retrying")
		}
		result = normalizeToolResult(result)
		result.CallID = call.ID
		result.DurationMS = time.Since(started).Milliseconds()
	}()
	if err := ctx.Err(); err != nil {
		return toolFailure("TOOL_CANCELED", "tool canceled before dispatch")
	}
	tool, ok := set.byName[call.Function.Name]
	if !ok {
		return toolFailure("UNKNOWN_TOOL", "tool is not explicitly allowed for this step")
	}
	value, err := decodeArguments([]byte(call.Function.Arguments))
	if err == nil {
		err = tool.schema.validate(value)
	}
	if err != nil {
		return toolFailure("INVALID_ARGUMENTS", err.Error())
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return toolFailure("INVALID_ARGUMENTS", "invalid tool arguments")
	}
	if tool.definition.Validate != nil {
		if err := tool.definition.Validate(append(json.RawMessage(nil), canonical...)); err != nil {
			return toolFailure("INVALID_ARGUMENTS", err.Error())
		}
	}
	invocation := ToolExecution{Step: set.step, CallID: call.ID, Name: call.Function.Name, Arguments: canonical}
	guards := append(append([]ToolGuard(nil), e.guards...), tool.definition.Guard)
	for _, guard := range guards {
		if guard == nil {
			continue
		}
		copy := invocation
		copy.Arguments = append(json.RawMessage(nil), canonical...)
		if err := guard(ctx, copy); err != nil {
			return toolFailure("TOOL_DENIED", err.Error())
		}
	}
	if ctx.Err() != nil {
		return toolFailure("TOOL_CANCELED", "tool canceled before dispatch")
	}
	execCtx := context.WithValue(ctx, toolCallKey{}, call.ID)
	cancel := func() {}
	if tool.definition.Timeout > 0 {
		execCtx, cancel = context.WithTimeout(execCtx, tool.definition.Timeout)
	}
	defer cancel()
	result, err = tool.definition.Execute(execCtx, canonical)
	if err != nil {
		code := "TOOL_FAILED"
		if errors.Is(err, context.DeadlineExceeded) {
			code = "TOOL_TIMEOUT"
		} else if errors.Is(err, context.Canceled) {
			code = "TOOL_CANCELED"
		}
		result = toolFailure(code, err.Error())
	}
	// A committed successful write stays successful even if cancellation raced
	// its return. Do not pretend it was rolled back or invite an automatic retry.
	return result
}

func toolFailure(code, message string) ToolResult { return ToolResult{Error: message, Code: code} }

func normalizeToolResult(r ToolResult) ToolResult {
	if r.Error != "" {
		if r.Code == "" {
			r.Code = "TOOL_FAILED"
		}
		r.Error = boundToolText(r.Error, 4096)
		r.Output = ""
		r.Value = nil
		return r
	}
	if len(r.Value) > 0 && !json.Valid(r.Value) {
		return toolFailure("INVALID_TOOL_RESULT", "tool returned invalid structured data; inspect saved state before retrying")
	}
	if len(r.Value) == 0 {
		if json.Valid([]byte(r.Output)) {
			r.Value = append(json.RawMessage(nil), r.Output...)
		} else {
			r.Value, _ = json.Marshal(r.Output)
		}
	} else {
		r.Value = append(json.RawMessage(nil), r.Value...)
		if r.Output == "" {
			r.Output = string(r.Value)
		}
	}
	const maxOutput = 32 << 10
	if len(r.Output) > maxOutput {
		r.Output = boundToolText(r.Output, maxOutput) + "\n[Tool completed; result text truncated. Read specific records if needed.]"
		r.Truncated = true
	}
	if len(r.Value) > 256<<10 {
		r.Value = nil
		r.Truncated = true
	}
	return r
}

func boundToolText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	s = s[:limit]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func (r ToolResult) modelContent() string {
	if r.Error != "" {
		return "Error: [" + r.Code + "] " + r.Error
	}
	return r.Output
}
