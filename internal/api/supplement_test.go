package api

import (
	"context"
	"testing"

	"chat-agent/internal/probe"
)

// A message joins the running turn only when the sender asked for it. The record
// is kept but stops being queued, which is what keeps a retry idempotent while
// still preventing the same words from running as their own turn.
func TestExplicitSupplementJoinsTheRunningTurn(t *testing.T) {
	s, id, _, release := supplementFixture(t)
	defer release()

	turn, _, err := s.SubmitTurn(context.Background(), TurnSubmission{
		SessionID:  id,
		RequestID:  "supplement-explicit-1",
		Content:    "只处理前五页",
		Supplement: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if turn.QueueDisposition != "current_task" {
		t.Fatalf("queue disposition = %q, want current_task", turn.QueueDisposition)
	}
	items, _, err := s.turns.List(id)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range items {
		if item.ID != turn.ID {
			continue
		}
		found = true
		if item.Status != "supplement" {
			t.Fatalf("accepted supplement status = %q, want supplement", item.Status)
		}
		if item.Content != "只处理前五页" {
			t.Fatalf("the words the user typed must survive: %q", item.Content)
		}
	}
	if !found {
		t.Fatal("the record must be kept so a retry with the same id stays idempotent")
	}
	pending, _, err := s.turns.HasQueued(id)
	if err != nil {
		t.Fatal(err)
	}
	if pending {
		t.Fatal("an accepted supplement must not stay queued, or it runs twice")
	}
}

// The reported defect: the original request was deleted, so a retry lost its
// deduplication evidence and the correction could be applied again.
func TestRepeatedSupplementReturnsTheOriginalOutcome(t *testing.T) {
	s, id, _, release := supplementFixture(t)
	defer release()

	first, _, err := s.SubmitTurn(context.Background(), TurnSubmission{
		SessionID: id, RequestID: "supplement-retry-1", Content: "只看第三章", Supplement: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.QueueDisposition != "current_task" {
		t.Fatalf("first submission = %q, want current_task", first.QueueDisposition)
	}
	second, duplicate, err := s.SubmitTurn(context.Background(), TurnSubmission{
		SessionID: id, RequestID: "supplement-retry-1", Content: "只看第三章", Supplement: true,
	})
	if err != nil {
		t.Fatalf("a retry of the same request must not fail: %v", err)
	}
	if !duplicate {
		t.Fatal("the same request id must be recognised as a duplicate")
	}
	if second.QueueDisposition != "current_task" {
		t.Fatalf("retry disposition = %q, want the original current_task", second.QueueDisposition)
	}
}

// If the runtime refuses the correction after the record was written, the request
// goes back in the queue. Losing it would be worse than running it a round later.
func TestRefusedSupplementReturnsToTheQueue(t *testing.T) {
	s, id, agent, release := supplementFixture(t)
	defer release()

	// Fill the runtime's queue so the next correction cannot be taken.
	for i := 0; i < 4; i++ {
		if !agent.AddSupplement("filler", "correction") {
			t.Fatalf("filler %d should fit", i)
		}
	}
	turn, _, err := s.SubmitTurn(context.Background(), TurnSubmission{
		SessionID: id, RequestID: "supplement-refused-1", Content: "改成只列结论", Supplement: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if turn.QueueDisposition == "current_task" {
		t.Fatal("a refused correction cannot report having joined the turn")
	}
	pending, _, err := s.turns.HasQueued(id)
	if err != nil {
		t.Fatal(err)
	}
	if !pending {
		t.Fatal("a refused correction must return to the queue instead of vanishing")
	}
}

// Without the flag the message stays an ordinary queued turn. This is the case
// that used to break: inferring that every later message was a correction made
// "also check the weather" disappear.
func TestUnflaggedMessageStillQueues(t *testing.T) {
	s, id, _, release := supplementFixture(t)
	defer release()

	turn, _, err := s.SubmitTurn(context.Background(), TurnSubmission{
		SessionID: id,
		RequestID: "supplement-ordinary-1",
		Content:   "顺便查一下明天的天气",
	})
	if err != nil {
		t.Fatal(err)
	}
	if turn.QueueDisposition == "current_task" {
		t.Fatal("a message without the flag must not be folded into the running turn")
	}
	items, _, err := s.turns.List(id)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range items {
		if item.ID == turn.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("an ordinary message must stay in the queue")
	}
}

// With no turn running there is nothing to join, so an explicit supplement still
// has to queue: dropping it would lose the user's text.
func TestSupplementQueuesWhenNoTurnIsRunning(t *testing.T) {
	s, id, _, release := supplementFixture(t)
	release() // the run is over before the message arrives

	turn, _, err := s.SubmitTurn(context.Background(), TurnSubmission{
		SessionID: id,
		RequestID: "supplement-idle-1",
		Content:   "改成只要结论",
		Supplement: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if turn.QueueDisposition == "current_task" {
		t.Fatal("nothing was running, so the message cannot have joined a turn")
	}
}

func supplementFixture(t *testing.T) (*Server, string, *probe.Agent, func()) {
	t.Helper()
	cfg := probe.DefaultConfig()
	cfg.Demo = true
	s, err := New(Config{Agent: cfg, DataDir: t.TempDir(), AccessToken: "supplement-test-access-token-past-32"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	const id = "supplement_session"
	if err := s.runtime.Create(id, "companion"); err != nil {
		t.Fatal(err)
	}
	sess, err := s.session(id)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := s.agent(sess, "", id)
	if err != nil {
		t.Fatal(err)
	}
	// Stand in for a turn that is already in flight.
	done := make(chan struct{})
	s.mu.Lock()
	s.runs[id] = &runState{cancel: func() {}, done: done, agent: agent}
	s.mu.Unlock()
	release := func() {
		s.mu.Lock()
		if s.runs[id] != nil {
			delete(s.runs, id)
			close(done)
		}
		s.mu.Unlock()
	}
	return s, id, agent, release
}
