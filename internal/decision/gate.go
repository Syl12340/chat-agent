package decision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"pocket-agent/internal/probe"
)

// Gate is the host-side enforcement point for the decision service.
//
// It is not a tool and the model cannot call it directly: it runs inside the tool
// executor, after static guards and before dispatch. When a tool is declared
// required, this gate decides — not the model — whether an evaluation happens.
type Gate struct {
	client *Client
	policy Policy
}

// Policy is the acceptance rule applied to a verdict.
//
// The numbers are deliberately conservative rather than tuned: ecnu-decision's
// `confidence` is the winning class probability, not a calibrated accuracy. Until
// real labelled tasks exist, an unsure answer should route to the user instead of
// being read as permission.
type Policy struct {
	// MinConfidence is the winning probability below which a verdict is treated as
	// inconclusive.
	MinConfidence float64
	// MinMargin is the gap between winner and runner-up below which a verdict is
	// treated as inconclusive.
	MinMargin float64
	// Timeout bounds one evaluation. The endpoint is stateless, so a timeout is
	// safe to retry once.
	Timeout time.Duration
}

// DefaultPolicy is intentionally strict: most of the value is in not proceeding on
// a split decision.
func DefaultPolicy() Policy {
	return Policy{MinConfidence: 0.70, MinMargin: 0.30, Timeout: 12 * time.Second}
}

// NewGate returns nil when no client is configured, matching New's contract: an
// unconfigured provider must not exist rather than exist and fail.
func NewGate(client *Client, policy Policy) *Gate {
	if client == nil {
		return nil
	}
	if policy.MinConfidence <= 0 {
		policy.MinConfidence = 0.70
	}
	if policy.MinMargin <= 0 {
		policy.MinMargin = 0.30
	}
	if policy.Timeout <= 0 {
		policy.Timeout = 12 * time.Second
	}
	return &Gate{client: client, policy: policy}
}

// Gate implements probe.DecisionGate.
func (g *Gate) Gate(ctx context.Context, execution probe.ToolExecution, requirement probe.DecisionRequirement, risk probe.RiskLevel) (*probe.DecisionRecord, error) {
	record := &probe.DecisionRecord{Required: requirement, Risk: risk}
	if g == nil || g.client == nil {
		record.Reason = "未配置决策服务"
		record.Degraded = true
		return record, errors.New("未配置决策服务")
	}
	if requirement == probe.DecisionNever {
		record.Outcome = "skipped"
		return record, nil
	}

	request, question := g.build(execution, requirement, risk)
	record.Question = question

	// One bounded retry: evaluation has no side effects, so a transport failure is
	// safe to repeat once. Business actions are never retried this way.
	result, err := g.evaluate(ctx, request)
	if err != nil {
		record.Reason = err.Error()
		record.Degraded = true
		return record, err
	}
	answer, found := result.Answers["verdict"]
	if !found {
		record.Reason = "决策服务没有返回判定"
		record.Degraded = true
		return record, errors.New("决策服务没有返回判定")
	}
	record.Labels = answer.Labels
	record.Choice = answer.Choice
	record.Confidence = answer.Confidence
	record.RunnerUp, record.Margin = runnerUp(answer)

	allowed, reason := g.judge(answer)
	record.Reason = reason
	if allowed {
		record.Outcome = "allowed"
		return record, nil
	}
	record.Outcome = "refused"
	return record, errors.New(reason)
}

// evaluate performs the call with a single retry on transport failure.
func (g *Gate) evaluate(ctx context.Context, request Request) (*Result, error) {
	callCtx, cancel := context.WithTimeout(ctx, g.policy.Timeout)
	defer cancel()
	result, err := g.client.Decide(callCtx, request)
	if err == nil {
		return result, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	retryCtx, retryCancel := context.WithTimeout(ctx, g.policy.Timeout)
	defer retryCancel()
	result, retryErr := g.client.Decide(retryCtx, request)
	if retryErr == nil {
		return result, nil
	}
	return nil, fmt.Errorf("决策服务不可用：%w", err)
}

// judge turns a probability distribution into a gate outcome.
//
// Rules, in order:
//   - an explicit refuse label refuses;
//   - an explicit insufficient-evidence label routes to the user;
//   - a weak winner or a narrow margin routes to the user;
//   - only a clear proceed allows the call.
//
// "Route to the user" means the executor returns needs-approval, so the authority
// is the person, never a coin flip by the assessor.
func (g *Gate) judge(answer Answer) (bool, string) {
	switch answer.Choice {
	case "refuse":
		return false, "独立评估认为这项操作不应执行"
	case "insufficient_evidence":
		return false, "独立评估认为证据不足，无法判断"
	}
	if answer.Confidence < g.policy.MinConfidence {
		return false, fmt.Sprintf("独立评估置信度偏低（%.2f），不足以支持执行", answer.Confidence)
	}
	// runnerUp already returns the gap; subtracting again would compare the winner
	// against itself.
	if _, margin := runnerUp(answer); margin < g.policy.MinMargin {
		return false, fmt.Sprintf("独立评估在有分歧的情况下胜出（差值 %.2f），不足以支持执行", margin)
	}
	if answer.Choice != "proceed" {
		return false, "独立评估未给出可以执行的结论"
	}
	return true, "独立评估认为可以执行"
}

// runnerUp returns the second most likely label and the gap to the winner.
func runnerUp(answer Answer) (string, float64) {
	name, value := "", 0.0
	for label, probability := range answer.Labels {
		if label == answer.Choice {
			continue
		}
		if probability > value {
			name, value = label, probability
		}
	}
	return name, answer.Confidence - value
}

// build turns a tool call into one decision question plus the state that supports it.
//
// The label set always includes an escape hatch. `choice` has no allow_none, so
// without an explicit insufficient_evidence label the endpoint would be forced to
// pick a side it has no basis for.
func (g *Gate) build(execution probe.ToolExecution, requirement probe.DecisionRequirement, risk probe.RiskLevel) (Request, string) {
	arguments := string(execution.Arguments)
	if len(arguments) > 4000 {
		arguments = arguments[:4000] + "…"
	}
	state := map[string]any{
		"tool":    execution.Name,
		"risk":    string(risk),
		"step":    execution.Step.Number,
		"arguments": json.RawMessage(execution.Arguments),
		"note": "arguments are the exact parameters the assistant proposed",
	}
	if execution.Step.RoleID != "" {
		state["role"] = execution.Step.RoleID
	}

	instructions := fmt.Sprintf(
		"判断这次工具调用是否应当被执行。工具 %s 被标记为风险等级 %s，调用参数见 state.arguments。"+
			"你只评估是否应当继续，不执行操作，也不改写参数。"+
			"若参数与工具用途明显不符、目标范围不清、或是一次不可逆或高影响操作而缺少足够依据，选择 refuse 或 insufficient_evidence。",
		execution.Name, riskLabel(risk))

	questions := map[string]Question{
		"verdict": {
			Type:         TypeChoice,
			Instructions: instructions,
			Criteria: map[string]string{
				"proceed":               "参数与工具用途一致，范围明确，可以执行",
				"refuse":                "存在明显问题，不应执行",
				"insufficient_evidence": "信息不足以判断，应改为向用户确认",
			},
		},
	}
	return Request{State: state, Questions: questions}, strings.TrimSpace(instructions)
}

func riskLabel(risk probe.RiskLevel) string {
	switch risk {
	case probe.RiskCritical:
		return "极高（不可逆或影响全局）"
	case probe.RiskHigh:
		return "高（有破坏性或影响他人）"
	case probe.RiskMedium:
		return "中（可恢复但影响可见）"
	case probe.RiskLow:
		return "低"
	case probe.RiskNone, "":
		return "无"
	}
	return string(risk)
}
