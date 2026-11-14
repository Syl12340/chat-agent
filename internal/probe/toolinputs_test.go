package probe

import (
	"context"
	"testing"
)

// The tool context is the only channel through which a tool learns who is
// asking and what the user actually said. Nothing here may be inferred from
// model output, so the boundaries are worth asserting directly.
func TestToolContextCarriesTrustedScopeOnly(t *testing.T) {
	ctx := ToolContext(context.Background(), "session-1", "companion", "call-1", "op-1")
	ctx = WithToolInputs(ctx, []Message{
		{Role: "user", Content: "以后每天九点跟我说早安"},
		{Role: "assistant", Content: "好的，我记住了"},
		{Role: "user", Content: "顺便记一下周末的事"},
	})
	if got := ToolRole(ctx); got != "companion" {
		t.Fatalf("role = %q", got)
	}
	if got := ToolSession(ctx); got != "session-1" {
		t.Fatalf("session = %q", got)
	}
	if got := ToolCallID(ctx); got != "call-1" {
		t.Fatalf("call = %q", got)
	}
	if got := ToolOperationID(ctx); got != "op-1" {
		t.Fatalf("operation = %q", got)
	}
	inputs := ToolInputs(ctx)
	if len(inputs) != 2 || inputs[0] != "以后每天九点跟我说早安" || inputs[1] != "顺便记一下周末的事" {
		t.Fatalf("inputs = %q, assistant text must not become user input", inputs)
	}
	if !TurnHasEvidence(inputs, "每天九点跟我说早安") {
		t.Fatal("a real user quote was not accepted")
	}
	if TurnHasEvidence(inputs, "好的，我记住了") {
		t.Fatal("assistant text passed as user evidence")
	}
	if TurnHasEvidence(inputs, "每天八点跟我说晚安") {
		t.Fatal("a paraphrase passed as a quote")
	}
	if TurnHasEvidence(inputs, "  ") || TurnHasEvidence(inputs, "") {
		t.Fatal("an empty quote passed as evidence")
	}
}
