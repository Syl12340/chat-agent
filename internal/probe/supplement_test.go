package probe

import "testing"

// A correction may only be folded in where a user message can legally follow.
// Inserting one right after an assistant tool call would separate that call from
// its results, which the transcript does not allow.
func TestTakeSupplementsWaitsForALegalBoundary(t *testing.T) {
	a := &Agent{}
	if !a.AddSupplement("req-1", "  only the first five pages  ") {
		t.Fatal("a plain correction should be accepted")
	}
	if got := a.takeSupplements(Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "call-1"}}}); got != nil {
		t.Fatalf("must not inject between a tool call and its results, got %v", got)
	}
	got := a.takeSupplements(Message{Role: "tool"})
	if len(got) != 1 || got[0].content != "only the first five pages" || got[0].requestID != "req-1" {
		t.Fatalf("takeSupplements = %v, want the trimmed correction and its request id", got)
	}
	if again := a.takeSupplements(Message{Role: "tool"}); again != nil {
		t.Fatalf("a supplement must be consumed once, got %v again", again)
	}
}

func TestAddSupplementRejectsEmptyAndCapsTheQueue(t *testing.T) {
	a := &Agent{}
	if a.AddSupplement("req-1", "   ") {
		t.Fatal("blank text is not an instruction")
	}
	for i := 0; i < 4; i++ {
		if !a.AddSupplement("req-1", "correction") {
			t.Fatalf("supplement %d should fit", i)
		}
	}
	if a.AddSupplement("req-1", "one too many") {
		t.Fatal("the queue is bounded, otherwise a stuck turn accumulates forever")
	}
}

// The reported defect: a correction that only lived inside one model request was
// lost by the next step. It has to become a real message with a durable id.
func TestAppliedSupplementsBecomeRealMessages(t *testing.T) {
	session := NewSession()
	agent := &Agent{session: session}
	if !agent.AddSupplement("req-1", "only the first five pages") {
		t.Fatal("the correction should be accepted")
	}
	applied := agent.takeSupplements(Message{Role: "user"})
	if err := agent.applySupplements(applied); err != nil {
		t.Fatal(err)
	}
	messages := session.Messages()
	if len(messages) != 1 {
		t.Fatalf("the transcript should hold the correction, got %d messages", len(messages))
	}
	if messages[0].Role != "user" || messages[0].Origin != supplementOrigin {
		t.Fatalf("a correction is a user message with its own origin: %+v", messages[0])
	}
	if messages[0].ID == "" {
		t.Fatal("a persisted message needs a durable id")
	}
	if messages[0].Content != "only the first five pages" {
		t.Fatalf("the stored text must be exactly what was typed: %q", messages[0].Content)
	}
	// A later assembly reads the transcript, so the correction is still there for
	// the next model step instead of being consumed once.
	visible := session.ContextMessagesFrom(0)
	found := false
	for _, message := range visible {
		if message.Origin == supplementOrigin && message.Content == "only the first five pages" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the correction did not survive into later context: %+v", visible)
	}
}
