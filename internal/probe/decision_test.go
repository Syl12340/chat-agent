package probe

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// stubGate records what it was asked and answers with a fixed verdict.
type stubGate struct {
	called   int
	record   *DecisionRecord
	err      error
	lastName string
	lastArgs json.RawMessage
	lastReq  DecisionRequirement
	lastRisk RiskLevel
}

func (g *stubGate) Gate(ctx context.Context, execution ToolExecution, requirement DecisionRequirement, risk RiskLevel) (*DecisionRecord, error) {
	g.called++
	g.lastName = execution.Name
	g.lastArgs = execution.Arguments
	g.lastReq = requirement
	g.lastRisk = risk
	return g.record, g.err
}

func decisionTool(name string, requirement DecisionRequirement, risk RiskLevel) Tool {
	return Tool{
		Name:        name,
		Description: "test tool",
		Parameters: json.RawMessage(`{"type":"object","properties":{"confirmed_by_user":{"type":"boolean"},"target":{"type":"string"}},"additionalProperties":false}`),
		Timeout:     0,
		Decision:    requirement,
		Risk:        risk,
		Execute: func(context.Context, json.RawMessage) (ToolResult, error) {
			return ToolResult{Output: "done"}, nil
		},
	}
}

func executeOnce(t *testing.T, tool Tool, gate DecisionGate, args string) ToolResult {
	t.Helper()
	return executeApproved(t, tool, gate, nil, args)
}

func executeApproved(t *testing.T, tool Tool, gate DecisionGate, approvals *ApprovalStore, args string) ToolResult {
	t.Helper()
	registry, err := NewToolRegistry([]Tool{tool})
	if err != nil {
		t.Fatal(err)
	}
	step := StepContext{RoleID: "companion", SessionID: "s1", Number: 1, CallsLeft: 4, ToolsAllowed: true}
	set, err := registry.Resolve(context.Background(), step)
	if err != nil {
		t.Fatal(err)
	}
	executor := ToolExecutor{gate: gate, approvals: approvals}
	call := ToolCall{ID: "call_1"}
	call.Function.Name = tool.Name
	call.Function.Arguments = args
	result, _ := executor.Execute(context.Background(), set, call, nil, 0)
	return result
}

// The default must be inert: adding the gate cannot change behaviour for tools
// that never asked for it.
func TestDecisionNeverSkipsTheGateEntirely(t *testing.T) {
	gate := &stubGate{}
	result := executeOnce(t, decisionTool("reader", DecisionNever, RiskNone), gate, `{}`)
	if result.Error != "" {
		t.Fatalf("unexpected failure: %v", result.Error)
	}
	if gate.called != 0 {
		t.Fatalf("a never-tool must not reach the gate, got %d calls", gate.called)
	}
	if result.Decision != nil {
		t.Fatal("no decision record should be attached")
	}
}

func TestUnsetDecisionBehavesAsNever(t *testing.T) {
	gate := &stubGate{}
	result := executeOnce(t, decisionTool("legacy", "", ""), gate, `{}`)
	if result.Error != "" || gate.called != 0 {
		t.Fatalf("zero value must behave like never: err=%v calls=%d", result.Error, gate.called)
	}
}

// A missing opinion is not evidence of danger: advisory work keeps flowing.
func TestAdvisorySurvivesUnavailableService(t *testing.T) {
	gate := &stubGate{err: errors.New("决策服务不可用")}
	result := executeOnce(t, decisionTool("memory_write", DecisionAdvisory, RiskLow), gate, `{}`)
	if result.Error != "" {
		t.Fatalf("advisory must not block on an unavailable service: %v", result.Error)
	}
	if result.Decision == nil || result.Decision.Outcome != "allowed" {
		t.Fatalf("advisory outcome should be allowed: %+v", result.Decision)
	}
}

// The core safety rule: required + no verdict = ask the person, never pass silently.
func TestRequiredWithoutServiceNeedsApproval(t *testing.T) {
	gate := &stubGate{err: errors.New("决策服务不可用")}
	result := executeOnce(t, decisionTool("workspace_delete", DecisionRequired, RiskHigh), gate, `{}`)
	if result.Code != "DECISION_NEEDS_APPROVAL" {
		t.Fatalf("expected DECISION_NEEDS_APPROVAL, got %q (%s)", result.Code, result.Error)
	}
	if result.Decision == nil || result.Decision.Outcome != "needs_approval" {
		t.Fatalf("record should say needs_approval: %+v", result.Decision)
	}
	if !result.Decision.Degraded {
		t.Fatal("record should be marked degraded")
	}
	if !strings.Contains(result.Error, "界面") {
		t.Fatalf("error should tell the model where the human decides: %s", result.Error)
	}
	if result.Decision.Approval == nil || result.Decision.Approval.Digest == "" {
		t.Fatalf("record should name the exact call a human may authorize: %+v", result.Decision)
	}
}

// The model cannot authorize itself: a boolean in the arguments is just data.
func TestModelCannotSelfAuthorize(t *testing.T) {
	store := NewApprovalStore()
	gate := &stubGate{err: errors.New("决策服务不可用")}
	result := executeApproved(t, decisionTool("workspace_delete", DecisionRequired, RiskHigh), gate, store, `{"confirmed_by_user":true}`)
	if result.Code != "DECISION_NEEDS_APPROVAL" {
		t.Fatalf("a model-written flag must not authorize anything, got %q (%s)", result.Code, result.Error)
	}
}

// A host ticket covers exactly one call: the same tool, the same arguments, once.
func TestHostTicketCoversOneExactCall(t *testing.T) {
	tool := decisionTool("workspace_delete", DecisionRequired, RiskHigh)
	store := NewApprovalStore()
	refused := executeApproved(t, tool, &stubGate{err: errors.New("决策服务不可用")}, store, `{"target":"a"}`)
	if refused.Code != "DECISION_NEEDS_APPROVAL" {
		t.Fatalf("setup: expected a refusal, got %q", refused.Code)
	}
	if _, err := store.Issue("s1", "companion", "workspace_delete", refused.Decision.Approval.Digest); err != nil {
		t.Fatalf("a human confirmation should be accepted for a refused call: %v", err)
	}
	ran := executeApproved(t, tool, &stubGate{err: errors.New("决策服务不可用")}, store, `{"target":"a"}`)
	if ran.Error != "" {
		t.Fatalf("the authorized call must run: %v", ran.Error)
	}
	if ran.Decision == nil || ran.Decision.Outcome != "user_override" || ran.Decision.ApprovalID == "" {
		t.Fatalf("record should mark the host override with its ticket: %+v", ran.Decision)
	}
	// Authorization is single-use: a retry must ask again.
	again := executeApproved(t, tool, &stubGate{err: errors.New("决策服务不可用")}, store, `{"target":"a"}`)
	if again.Code != "DECISION_NEEDS_APPROVAL" {
		t.Fatalf("a consumed ticket must not authorize a second run, got %q", again.Code)
	}
}

// Broader arguments are a different operation and need their own approval.
func TestTicketDoesNotCoverChangedArguments(t *testing.T) {
	tool := decisionTool("workspace_delete", DecisionRequired, RiskHigh)
	store := NewApprovalStore()
	refused := executeApproved(t, tool, &stubGate{err: errors.New("决策服务不可用")}, store, `{"target":"a"}`)
	if _, err := store.Issue("s1", "companion", "workspace_delete", refused.Decision.Approval.Digest); err != nil {
		t.Fatal(err)
	}
	changed := executeApproved(t, tool, &stubGate{err: errors.New("决策服务不可用")}, store, `{"target":"b"}`)
	if changed.Code != "DECISION_NEEDS_APPROVAL" {
		t.Fatalf("a ticket for one payload must not cover another, got %q", changed.Code)
	}
}

// Another session or role cannot spend somebody else's ticket.
func TestTicketIsBoundToSessionAndRole(t *testing.T) {
	store := NewApprovalStore()
	store.NoteDenied("s1", "companion", "workspace_delete", "digest", "no service")
	if _, err := store.Issue("s2", "companion", "workspace_delete", "digest"); err == nil {
		t.Fatal("a different session must not be able to issue against another session's refusal")
	}
	if _, err := store.Issue("s1", "stranger", "workspace_delete", "digest"); err == nil {
		t.Fatal("a different role must not be able to issue against another role's refusal")
	}
	if _, ok := store.Consume("s2", "companion", "workspace_delete", "digest"); ok {
		t.Fatal("consuming under another session must fail even if a ticket exists")
	}
}

// An expired ticket is not authorization.
func TestExpiredTicketIsRejected(t *testing.T) {
	store := NewApprovalStore()
	now := time.Unix(1700000000, 0)
	store.now = func() time.Time { return now }
	store.NoteDenied("s1", "companion", "workspace_delete", "digest", "no service")
	if _, err := store.Issue("s1", "companion", "workspace_delete", "digest"); err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return now.Add(DefaultApprovalTTL + time.Second) }
	if _, ok := store.Consume("s1", "companion", "workspace_delete", "digest"); ok {
		t.Fatal("an expired ticket must not authorize anything")
	}
}

// Malformed arguments never reach the gate: schema validation runs first.
func TestInvalidArgumentsFailBeforeTheGate(t *testing.T) {
	gate := &stubGate{}
	result := executeOnce(t, decisionTool("workspace_delete", DecisionRequired, RiskHigh), gate, `not json`)
	if result.Code != "INVALID_ARGUMENTS" {
		t.Fatalf("expected INVALID_ARGUMENTS, got %q", result.Code)
	}
	if gate.called != 0 {
		t.Fatal("the gate must not run on arguments that already failed validation")
	}
}

func TestRequiredWithHealthyGateRuns(t *testing.T) {
	gate := &stubGate{record: &DecisionRecord{Required: DecisionRequired, Risk: RiskHigh, Outcome: "allowed", Choice: "proceed", Confidence: 0.91}}
	result := executeOnce(t, decisionTool("workspace_delete", DecisionRequired, RiskHigh), gate, `{}`)
	if result.Error != "" {
		t.Fatalf("a cleared call must run: %v", result.Error)
	}
	if result.Decision == nil || result.Decision.Choice != "proceed" {
		t.Fatalf("decision should ride along with the result: %+v", result.Decision)
	}
}

func TestRequiredWithRefusalBlocks(t *testing.T) {
	gate := &stubGate{record: &DecisionRecord{Required: DecisionRequired, Risk: RiskHigh, Outcome: "refused", Reason: "目标范围不清"}}
	result := executeOnce(t, decisionTool("workspace_delete", DecisionRequired, RiskHigh), gate, `{}`)
	if result.Code != "DECISION_REFUSED" {
		t.Fatalf("expected DECISION_REFUSED, got %q", result.Code)
	}
	if !strings.Contains(result.Error, "目标范围不清") {
		t.Fatalf("refusal reason should survive: %s", result.Error)
	}
}

// A live verdict wins: a human override of the missing opinion is not a veto of
// the assessor's answer.
func TestTicketDoesNotOverrideAHealthyRefusal(t *testing.T) {
	tool := decisionTool("workspace_delete", DecisionRequired, RiskHigh)
	store := NewApprovalStore()
	refused := executeApproved(t, tool, &stubGate{err: errors.New("决策服务不可用")}, store, `{}`)
	if _, err := store.Issue("s1", "companion", "workspace_delete", refused.Decision.Approval.Digest); err != nil {
		t.Fatal(err)
	}
	healthy := &stubGate{record: &DecisionRecord{Required: DecisionRequired, Risk: RiskHigh, Outcome: "refused", Reason: "不应执行"}}
	result := executeApproved(t, tool, healthy, store, `{}`)
	if result.Code != "DECISION_REFUSED" {
		t.Fatalf("a healthy refusal must still block, got %q", result.Code)
	}
}

// With no gate wired at all, required tools must not silently proceed.
func TestRequiredWithoutAnyGateNeedsApproval(t *testing.T) {
	result := executeOnce(t, decisionTool("workspace_delete", DecisionRequired, RiskHigh), nil, `{}`)
	if result.Code != "DECISION_NEEDS_APPROVAL" {
		t.Fatalf("a required tool with no gate must ask for approval, got %q (%s)", result.Code, result.Error)
	}
}

func TestRegistryRejectsUnknownDecisionMetadata(t *testing.T) {
	bad := decisionTool("weird", "sometimes", RiskHigh)
	if _, err := NewToolRegistry([]Tool{bad}); err == nil {
		t.Fatal("unknown decision requirement must be rejected at registration")
	}
	bad = decisionTool("weird", DecisionRequired, "catastrophic")
	if _, err := NewToolRegistry([]Tool{bad}); err == nil {
		t.Fatal("unknown risk level must be rejected at registration")
	}
}

// The summary is what the model actually reads; it must not read as certainty.
func TestDecisionSummaryKeepsTheDistribution(t *testing.T) {
	record := &DecisionRecord{
		Outcome: "allowed", Choice: "proceed", Confidence: 0.52, RunnerUp: "refuse", Margin: 0.08,
	}
	text := decisionSummary(record)
	if !strings.Contains(text, "0.52") || !strings.Contains(text, "refuse") {
		t.Fatalf("summary should show the runner-up: %s", text)
	}
	if !strings.Contains(text, "不是授权") {
		t.Fatalf("summary must deny that probability is permission: %s", text)
	}
	if summary := decisionSummary(&DecisionRecord{Outcome: "needs_approval", Reason: "超时"}); !strings.Contains(summary, "界面") {
		t.Fatalf("needs-approval summary must say where the human decides: %s", summary)
	}
}
