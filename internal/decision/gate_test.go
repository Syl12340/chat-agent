package decision

import (
	"testing"

	"pocket-agent/internal/probe"
)

func answer(choice string, labels map[string]float64) Answer {
	return Answer{Type: TypeChoice, Choice: choice, Labels: labels, Confidence: labels[choice]}
}

// A clear proceed is the only thing that lets a required call through.
func TestClearProceedAllows(t *testing.T) {
	gate := NewGate(&Client{}, DefaultPolicy())
	allowed, reason := gate.judge(answer("proceed", map[string]float64{"proceed": 0.91, "refuse": 0.06, "insufficient_evidence": 0.03}))
	if !allowed {
		t.Fatalf("clear proceed should allow: %s", reason)
	}
}

func TestExplicitRefuseBlocks(t *testing.T) {
	gate := NewGate(&Client{}, DefaultPolicy())
	if allowed, _ := gate.judge(answer("refuse", map[string]float64{"refuse": 0.8, "proceed": 0.2})); allowed {
		t.Fatal("refuse must block regardless of confidence")
	}
}

func TestInsufficientEvidenceRoutesToUser(t *testing.T) {
	gate := NewGate(&Client{}, DefaultPolicy())
	if allowed, _ := gate.judge(answer("insufficient_evidence", map[string]float64{"insufficient_evidence": 0.95, "proceed": 0.05})); allowed {
		t.Fatal("insufficient evidence must not authorize execution")
	}
}

// 0.52 over 0.44 is a tie, not a decision.
func TestNarrowMarginDoesNotAuthorize(t *testing.T) {
	gate := NewGate(&Client{}, DefaultPolicy())
	if allowed, reason := gate.judge(answer("proceed", map[string]float64{"proceed": 0.52, "refuse": 0.44})); allowed {
		t.Fatalf("a near-tie must not authorize: %s", reason)
	}
}

func TestLowConfidenceDoesNotAuthorize(t *testing.T) {
	gate := NewGate(&Client{}, DefaultPolicy())
	if allowed, reason := gate.judge(answer("proceed", map[string]float64{"proceed": 0.55, "refuse": 0.10})); allowed {
		t.Fatalf("low confidence must not authorize: %s", reason)
	}
}

func TestRunnerUpReportsTheGap(t *testing.T) {
	name, margin := runnerUp(answer("proceed", map[string]float64{"proceed": 0.7, "refuse": 0.25, "insufficient_evidence": 0.05}))
	if name != "refuse" {
		t.Fatalf("runner-up should be the second highest: %s", name)
	}
	if margin < 0.44 || margin > 0.46 {
		t.Fatalf("margin should be 0.45, got %.2f", margin)
	}
}

// The question must always offer an escape hatch: choice has no allow_none, so
// without an explicit label the service is forced to pick a side it cannot know.
func TestGateQuestionAlwaysOffersUncertainty(t *testing.T) {
	gate := NewGate(&Client{}, DefaultPolicy())
	request, question := gate.build(probe.ToolExecution{Name: "workspace_delete"}, probe.DecisionRequired, probe.RiskHigh)
	if question == "" {
		t.Fatal("the question text should be recorded for the audit trail")
	}
	verdict, ok := request.Questions["verdict"]
	if !ok {
		t.Fatal("missing verdict question")
	}
	criteria, ok := verdict.Criteria.(map[string]string)
	if !ok {
		t.Fatalf("unexpected criteria type %T", verdict.Criteria)
	}
	for _, label := range []string{"proceed", "refuse", "insufficient_evidence"} {
		if _, found := criteria[label]; !found {
			t.Fatalf("missing label %s", label)
		}
	}
	if _, ok := request.State.(map[string]any); !ok {
		t.Fatalf("state should carry the call context, got %T", request.State)
	}
}

func TestGateAbsentWhenUnconfigured(t *testing.T) {
	if NewGate(nil, DefaultPolicy()) != nil {
		t.Fatal("an unconfigured client must not produce a gate")
	}
}

func TestRiskLabelIsReadable(t *testing.T) {
	for _, level := range []probe.RiskLevel{probe.RiskNone, probe.RiskLow, probe.RiskMedium, probe.RiskHigh, probe.RiskCritical} {
		if riskLabel(level) == "" {
			t.Fatalf("risk %s has no description", level)
		}
	}
}
