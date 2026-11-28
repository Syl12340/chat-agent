package api

import (
	"context"
	"testing"

	"chat-agent/internal/probe"
)

// A message joins the running turn only when the sender asked for it, and then the
// queued copy has to go: leaving it means the model reads the same instruction
// twice, once inside the turn and once as its own task.
func TestExplicitSupplementJoinsTheRunningTurn(t *testing.T) {
	s, id, agent, release := supplementFixture(t)
	defer release()

	turn, _, err := s.SubmitTurn(context.Background(), TurnSubmission{
		SessionID: id,
		RequestID: "supplement-explicit-1",
		Content:   "只处理前五页",
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
	for _, item := range items {
		if item.ID == turn.ID {
			t.Fatal("the queued copy survived, so the instruction would run twice")
		}
	}
	// The instruction is now the running turn's business; the counter proves the
	// agent accepted it rather than silently dropping it.
	if !agent.AddSupplement("probe") {
		t.Fatal("agent stopped accepting supplements too early")
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
