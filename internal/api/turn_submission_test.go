package api

import (
	"context"
	"testing"
	"time"

	"chat-agent/internal/probe"
)

type completionCapture struct {
	items chan TurnCompletion
}

func (c completionCapture) TurnCompleted(_ context.Context, item TurnCompletion) error {
	c.items <- item
	return nil
}

func TestSubmitTurnAndCompletionObserver(t *testing.T) {
	agent := probe.DefaultConfig()
	agent.Demo = true
	capture := completionCapture{items: make(chan TurnCompletion, 1)}
	app, err := New(Config{
		Agent:                  agent,
		DataDir:                t.TempDir(),
		AccessToken:            "turn-submission-fixture-token-over-32-characters",
		TurnCompletionObserver: capture,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close(context.Background()) })
	const sessionID = "channel-session"
	if err := app.runtime.Create(sessionID, "companion"); err != nil {
		t.Fatal(err)
	}
	input := TurnSubmission{SessionID: sessionID, RequestID: "stable-request", Content: "你好", Mode: "auto"}
	first, duplicate, err := app.SubmitTurn(context.Background(), input)
	if err != nil || duplicate || first.ID != input.RequestID {
		t.Fatalf("first submission: turn=%#v duplicate=%v err=%v", first, duplicate, err)
	}
	second, duplicate, err := app.SubmitTurn(context.Background(), input)
	if err != nil || !duplicate || second.ID != first.ID {
		t.Fatalf("duplicate submission: turn=%#v duplicate=%v err=%v", second, duplicate, err)
	}
	select {
	case completed := <-capture.items:
		if completed.SessionID != sessionID || completed.RequestID != input.RequestID || len(completed.RequestIDs) != 1 || completed.RequestIDs[0] != input.RequestID || completed.Text == "" || completed.CompletedAt.IsZero() {
			t.Fatalf("completion = %#v", completed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("completion observer was not called")
	}
}
