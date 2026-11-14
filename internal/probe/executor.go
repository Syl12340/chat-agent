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
type ToolExecutor struct {
	guards []ToolGuard
	// gate is the host's mandatory decision checkpoint. Nil disables gating, which
	// is what demo mode and tests want; it must never be nil in a deployment that
	// declares required tools.
	gate DecisionGate
	// approvals holds host-issued, single-use authorizations. It is nil unless a
	// caller wires one; nothing is authorized by default.
	approvals *ApprovalStore
}

// SetDecisionGate wires the gate. Called once during assembly, before any step runs.
func (e *ToolExecutor) SetDecisionGate(gate DecisionGate) { e.gate = gate }

func (e *ToolExecutor) Execute(ctx context.Context, set *ToolSet, call ToolCall, recorder RunRecorder, step int) (result ToolResult, recordErr error) {
	started := time.Now()
	operationID := ""
	dispatched := false
	defer func() {
		if recover() != nil {
			result = toolFailure("TOOL_INTERNAL", "tool execution failed; inspect saved state before retrying")
		}
		result = normalizeToolResult(result)
		result.CallID = call.ID
		result.DurationMS = time.Since(started).Milliseconds()
		if dispatched && recorder != nil {
			if err := recorder.ToolFinished(context.WithoutCancel(ctx), step, call, operationID, result); err != nil {
				recordErr = recordingError("tool finish", err)
			}
		}
	}()
	if err := ctx.Err(); err != nil {
		return toolFailure("TOOL_CANCELED", "tool canceled before dispatch"), nil
	}
	tool, ok := set.byName[call.Function.Name]
	if !ok {
		return toolFailure("UNKNOWN_TOOL", "tool is not explicitly allowed for this step"), nil
	}
	value, err := decodeArguments([]byte(call.Function.Arguments))
	if err == nil {
		err = tool.schema.validate(value)
	}
	if err != nil {
		return toolFailure("INVALID_ARGUMENTS", err.Error()), nil
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return toolFailure("INVALID_ARGUMENTS", "invalid tool arguments"), nil
	}
	if tool.definition.Validate != nil {
		if err := tool.definition.Validate(append(json.RawMessage(nil), canonical...)); err != nil {
			return toolFailure("INVALID_ARGUMENTS", err.Error()), nil
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
			return toolFailure("TOOL_DENIED", err.Error()), nil
		}
	}
	if ctx.Err() != nil {
		return toolFailure("TOOL_CANCELED", "tool canceled before dispatch"), nil
	}
	// The gate sits after every static check and before the durable start record:
	// a refusal must leave no trace of a dispatched call, and it must not be
	// skippable by the model's own judgement.
	decision, decisionErr := e.gateDecision(ctx, invocation, tool.definition)
	if decisionErr != nil {
		code := "DECISION_REFUSED"
		if errors.Is(decisionErr, ErrNeedsApproval) {
			code = "DECISION_NEEDS_APPROVAL"
		}
		failure := toolFailure(code, decisionErr.Error())
		attachDecision(&failure, decision)
		// The call never dispatched, so no tool.finished event will carry the
		// decision. Record it directly, or a refusal leaves no audit trail.
		if record, ok := recorder.(DecisionRecorder); ok && decision != nil {
			_ = record.DecisionEvaluated(context.WithoutCancel(ctx), step, call, decision)
		}
		return failure, nil
	}
	if recorder != nil {
		operationID, err = recorder.ToolStarted(ctx, step, call, canonical, tool.definition.Receipt)
		if err != nil {
			return toolFailure("RUNTIME_RECORDING", "tool was not dispatched because its durable start record failed"), recordingError("tool start", err)
		}
	}
	dispatched = true
	execCtx := ToolContext(ctx, set.step.SessionID, set.step.RoleID, call.ID, operationID)
	cancel := func() {}
	if tool.definition.Timeout > 0 {
		execCtx, cancel = context.WithTimeout(execCtx, tool.definition.Timeout)
	}
	defer cancel()
	result, err = tool.definition.Execute(execCtx, canonical)
	attachDecision(&result, decision)
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
	return result, nil
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
