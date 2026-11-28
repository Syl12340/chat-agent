package probe

import "testing"

// A correction may only be folded in where a user message can legally follow.
// Inserting one right after an assistant tool call would separate that call from
// its results, which the transcript does not allow.
func TestTakeSupplementsWaitsForALegalBoundary(t *testing.T) {
	a := &Agent{}
	if !a.AddSupplement("  only the first five pages  ") {
		t.Fatal("a plain correction should be accepted")
	}
	if got := a.takeSupplements(Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "call-1"}}}); got != nil {
		t.Fatalf("must not inject between a tool call and its results, got %v", got)
	}
	got := a.takeSupplements(Message{Role: "tool"})
	if len(got) != 1 || got[0] != "only the first five pages" {
		t.Fatalf("takeSupplements = %v, want the trimmed correction", got)
	}
	if again := a.takeSupplements(Message{Role: "tool"}); again != nil {
		t.Fatalf("a supplement must be consumed once, got %v again", again)
	}
}

func TestAddSupplementRejectsEmptyAndCapsTheQueue(t *testing.T) {
	a := &Agent{}
	if a.AddSupplement("   ") {
		t.Fatal("blank text is not an instruction")
	}
	for i := 0; i < 4; i++ {
		if !a.AddSupplement("correction") {
			t.Fatalf("supplement %d should fit", i)
		}
	}
	if a.AddSupplement("one too many") {
		t.Fatal("the queue is bounded, otherwise a stuck turn accumulates forever")
	}
}
