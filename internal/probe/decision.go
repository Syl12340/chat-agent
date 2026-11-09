package probe

import "context"

// DecisionRequirement declares whether the host must consult the independent
// decision service before a tool runs.
//
// The distinction that matters: a requirement constrains one tool call, not the
// system. An unavailable decision service must never stop ordinary work — it may
// only block the specific operations that were declared to need it.
//
// Borrowed in spirit from the layered admission model of DeepSeek Harness (MIT);
// the policy here is this project's own.
type DecisionRequirement string

const (
	// DecisionNever never consults the decision service, and never blocks on it.
	// This is the default: a zero value must not change existing behaviour.
	DecisionNever DecisionRequirement = "never"

	// DecisionAdvisory lets the model consult the service if it wants. If the
	// service is unavailable the call proceeds, because a missing opinion is not
	// evidence of a problem.
	DecisionAdvisory DecisionRequirement = "advisory"

	// DecisionRequired means the operation must be evaluated before it runs. If
	// the service is unavailable the operation is refused; everything else keeps
	// working. Required never means "approved" — it means "checked".
	DecisionRequired DecisionRequirement = "required"
)

// Valid reports whether the value is one this build understands.
func (r DecisionRequirement) Valid() bool {
	switch r {
	case DecisionNever, DecisionAdvisory, DecisionRequired:
		return true
	}
	return false
}

// RiskLevel is the host's own estimate of what a failed or wrong call would cost.
// It is metadata for the gate and for the record; it does not authorize anything.
type RiskLevel string

const (
	RiskNone     RiskLevel = "none"
	RiskLow      RiskLevel = "low"
	RiskMedium   RiskLevel = "medium"
	RiskHigh     RiskLevel = "high"
	RiskCritical RiskLevel = "critical"
)

func (r RiskLevel) Valid() bool {
	switch r {
	case "", RiskNone, RiskLow, RiskMedium, RiskHigh, RiskCritical:
		return true
	}
	return false
}

// DecisionRecord is the durable trace of one gate decision. It is written with the
// tool result so a later reader can see what was asked, what came back, and why
// the call was allowed or refused.
type DecisionRecord struct {
	Required DecisionRequirement `json:"required"`
	Risk     RiskLevel           `json:"risk,omitempty"`
	// Outcome is "skipped", "allowed", "refused" or "unavailable".
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
	// Question is the single question posed to the service, kept verbatim.
	Question string `json:"question,omitempty"`
	// Choice is the winning label, with its probability and the runner-up.
	Choice     string  `json:"choice,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	RunnerUp   string  `json:"runner_up,omitempty"`
	Margin     float64 `json:"margin,omitempty"`
	// Labels keeps the whole distribution: a 51% winner over a 49% rival is a
	// different situation from 99% over 1%, and the difference must survive.
	Labels map[string]float64 `json:"labels,omitempty"`
	// Degraded marks outcomes reached without a live service.
	Degraded bool `json:"degraded,omitempty"`
	// Approval names the exact call a human may authorize when no verdict could
	// be obtained. Only the host computes the digest, from canonical arguments;
	// a payload the model wrote cannot name itself here.
	Approval *ApprovalRequest `json:"approval,omitempty"`
	// ApprovalID is the host-issued ticket that allowed this run, kept for audit.
	ApprovalID string `json:"approval_id,omitempty"`
}

// DecisionGate is the host-side seam. It runs after static guards and before the
// dispatch record, so a refusal leaves no started-tool trace.
//
// Implementations must treat an unavailable service as follows: never blocks
// DecisionNever or DecisionAdvisory, always blocks DecisionRequired.
type DecisionGate interface {
	// Gate returns a record for the log and an error that blocks the call.
	// A nil error means the call may proceed.
	Gate(ctx context.Context, execution ToolExecution, requirement DecisionRequirement, risk RiskLevel) (*DecisionRecord, error)
}

// DecisionRecorder is implemented by recorders that want the gate outcome even
// when the call never dispatched.
//
// A passing gate already rides along on ToolResult.Decision, so this exists for the
// other case: a refusal happens before ToolStarted and would otherwise leave no
// trace of why the operation did not run.
type DecisionRecorder interface {
	DecisionEvaluated(ctx context.Context, step int, call ToolCall, record *DecisionRecord) error
}

// decisionGateKey carries the gate through context so the executor need not be
// rebuilt wherever tools are executed.
type decisionGateKey struct{}

// WithDecisionGate attaches a gate to the context. A nil gate disables gating for
// that subtree, which is what tests and the demo mode want.
func WithDecisionGate(ctx context.Context, gate DecisionGate) context.Context {
	return context.WithValue(ctx, decisionGateKey{}, gate)
}

func decisionGateFrom(ctx context.Context) DecisionGate {
	if ctx == nil {
		return nil
	}
	gate, _ := ctx.Value(decisionGateKey{}).(DecisionGate)
	return gate
}
