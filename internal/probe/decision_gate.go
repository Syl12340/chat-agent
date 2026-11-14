package probe

import (
	"context"
	"errors"
	"fmt"
)

// ErrNeedsApproval means this call was declared to need an independent
// evaluation, none could be obtained, and no human has authorized this exact
// call.
//
// It is deliberately not a permanent refusal: the assessor is an advisor and the
// user is the authority. A missing opinion escalates to the person; it never
// passes silently and never blocks forever.
var ErrNeedsApproval = errors.New("这项操作需要独立评估，但评估服务当前不可用。请把具体风险和影响范围讲清楚；用户在界面上确认执行后，原样重试这一次调用（不要修改参数），不要替用户假设同意")

// gateDecision runs the host's decision checkpoint for one call.
//
// The contract, in order of importance:
//
//  1. DecisionNever and an absent gate cost nothing — no network, no latency.
//     This is why adding the gate cannot disturb existing work.
//  2. An unavailable service never blocks DecisionAdvisory: a missing opinion is
//     not evidence of danger.
//  3. An unavailable service escalates DecisionRequired to a human. Authorization
//     can only come from the host: a boolean the model wrote into the arguments
//     is not consent.
func (e *ToolExecutor) gateDecision(ctx context.Context, execution ToolExecution, tool Tool) (*DecisionRecord, error) {
	requirement := tool.Decision
	if requirement == "" {
		requirement = DecisionNever
	}
	if requirement == DecisionNever {
		if tool.ApprovalRequired {
			return e.requireHumanApproval(execution, &DecisionRecord{Required: DecisionNever, Risk: tool.Risk})
		}
		return nil, nil
	}
	gate := e.gate
	if gate == nil {
		// Fall back to a context-scoped gate so a caller can inject one without
		// rebuilding the executor (used by tests and per-session wiring).
		gate = decisionGateFrom(ctx)
	}
	if gate == nil {
		return e.degraded(execution, requirement, tool.Risk, "未配置决策服务")
	}
	record, err := gate.Gate(ctx, execution, requirement, tool.Risk)
	if record == nil {
		record = &DecisionRecord{Required: requirement, Risk: tool.Risk}
	}
	if err != nil {
		// Any failure to obtain a verdict is treated as unavailability, including a
		// timeout: an assessor that never answered has not approved anything.
		record.Degraded = true
		if record.Reason == "" {
			record.Reason = err.Error()
		}
		return e.degradedRecord(execution, record)
	}
	// The record's verdict is authoritative even when the gate returned no error:
	// an implementation that reports refused must block, not be silently upgraded.
	if record.Outcome == "refused" {
		reason := record.Reason
		if reason == "" {
			reason = "独立评估未通过，操作已拒绝"
		}
		return record, errors.New(reason)
	}
	if record.Outcome == "" {
		record.Outcome = "allowed"
	}
	if tool.ApprovalRequired {
		return e.requireHumanApproval(execution, record)
	}
	return record, nil
}

func (e *ToolExecutor) requireHumanApproval(execution ToolExecution, record *DecisionRecord) (*DecisionRecord, error) {
	digest := ApprovalDigest(execution.Name, execution.Arguments)
	roleID, sessionID := execution.Step.RoleID, execution.Step.SessionID
	if ticket, ok := e.approvals.Consume(sessionID, roleID, execution.Name, digest); ok {
		record.Outcome = "user_override"
		// Approval is the request to approve, not a record of having approved.
		// Leaving it set makes the client offer the button again for a call that
		// already ran, and that second click can only 409.
		record.Approval = nil
		record.ApprovalID = ticket.ID
		return record, nil
	}
	const reason = "该主机操作被配置为必须由用户逐次批准"
	record.Outcome = "needs_approval"
	record.Approval = &ApprovalRequest{Tool: execution.Name, Digest: digest, Reason: reason}
	e.approvals.NoteDenied(sessionID, roleID, execution.Name, digest, reason)
	return record, fmt.Errorf("%w（原因：%s）", ErrNeedsApproval, reason)
}

// degraded completes a record when no verdict was obtained.
func (e *ToolExecutor) degraded(execution ToolExecution, requirement DecisionRequirement, risk RiskLevel, reason string) (*DecisionRecord, error) {
	return e.degradedRecord(execution, &DecisionRecord{Required: requirement, Risk: risk, Reason: reason, Degraded: true})
}

// degradedRecord decides what a missing verdict means. Advisory work proceeds;
// required work needs a real person, and only a host-issued ticket for this exact
// argument digest counts.
func (e *ToolExecutor) degradedRecord(execution ToolExecution, record *DecisionRecord) (*DecisionRecord, error) {
	if record.Required != DecisionRequired {
		record.Outcome = "allowed"
		return record, nil
	}
	digest := ApprovalDigest(execution.Name, execution.Arguments)
	roleID, sessionID := execution.Step.RoleID, execution.Step.SessionID
	if ticket, ok := e.approvals.Consume(sessionID, roleID, execution.Name, digest); ok {
		record.Outcome = "user_override"
		// See requireHumanApproval: the approved call must not look pending.
		record.Approval = nil
		record.ApprovalID = ticket.ID
		return record, nil
	}
	reason := record.Reason
	if reason == "" {
		reason = "决策服务不可用"
	}
	record.Outcome = "needs_approval"
	record.Approval = &ApprovalRequest{Tool: execution.Name, Digest: digest, Reason: reason}
	// Remember the refusal so a later confirmation can be matched to this call.
	// The model cannot issue this record; only the executor can.
	e.approvals.NoteDenied(sessionID, roleID, execution.Name, digest, reason)
	return record, fmt.Errorf("%w（原因：%s）", ErrNeedsApproval, reason)
}

// attachDecision records the gate outcome on the result so it travels with the
// tool's own durable record instead of needing a second event stream.
func attachDecision(result *ToolResult, record *DecisionRecord) {
	if record == nil {
		return
	}
	result.Decision = record
}

// decisionSummary renders one line for the model. It carries the distribution, not
// just the winner: "0.52 over 0.44" must not read as certainty.
func decisionSummary(record *DecisionRecord) string {
	if record == nil {
		return ""
	}
	switch record.Outcome {
	case "skipped":
		return ""
	case "needs_approval":
		return "决策服务不可用（" + record.Reason + "）。这项操作风险较高，没有执行。" +
			"请向用户说明具体风险和影响范围；用户在界面上确认执行后，才可以原样重试这一次调用。"
	case "user_override":
		return "决策服务不可用，本次执行依据用户在界面上的明确授权放行，已记录单据编号。"
	case "refused":
		message := "决策检查未通过"
		if record.Reason != "" {
			message += "：" + record.Reason
		}
		return message + "。"
	}
	if record.Choice == "" {
		return ""
	}
	message := fmt.Sprintf("独立决策：%s（概率 %.2f", record.Choice, record.Confidence)
	if record.RunnerUp != "" {
		message += fmt.Sprintf("，次选 %s，差值 %.2f", record.RunnerUp, record.Margin)
	}
	return message + "）。概率不是授权，关键操作仍需用户确认。"
}
