package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"chat-agent/internal/companion"
	"chat-agent/internal/probe"
	"chat-agent/internal/turns"
)

func TestChatContinuationUsesPrivateHistoryWithoutPushLease(t *testing.T) {
	ctx := context.Background()
	model := &careCountModel{}
	config := probe.DefaultConfig()
	config.Model, config.Provider = "fixture", model
	capture := completionCapture{items: make(chan TurnCompletion, 1)}
	s, err := New(Config{Agent: config, DataDir: t.TempDir(), AccessToken: "continuation-fixture-token-over-32", TurnCompletionObserver: capture})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(ctx) })
	const role, session = "companion", "shared-chat"
	if err := s.runtime.Create(session, role); err != nil {
		t.Fatal(err)
	}
	if subject := continuationSubject("这事不用再聊了，下次继续说别的"); subject != "" {
		t.Fatalf("negative cue was interpreted as permission: %q", subject)
	}
	turn, _, err := s.SubmitTurn(ctx, TurnSubmission{SessionID: session, RequestID: "qq-turn",
		Content: "这场演出晚点再聊", Origin: turns.TurnOrigin{Kind: "channel", Platform: "qq-official",
			AccountID: "bot", ConversationKind: "private", ConversationID: "person", SenderID: "person"}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-capture.items:
	case <-time.After(5 * time.Second):
		t.Fatal("ordinary QQ turn did not settle")
	}
	s.mu.Lock()
	worker := s.runs[session]
	s.mu.Unlock()
	if worker != nil {
		select {
		case <-worker.done:
		case <-time.After(5 * time.Second):
			t.Fatal("turn worker did not release the session")
		}
	}
	p, err := s.life.Settings(role)
	if err != nil {
		t.Fatal(err)
	}
	probability := 100
	p.Enabled, p.AutoGenerate, p.AutoProbability = true, true, &probability
	p.QuietStart, p.QuietEnd = "00:00", "00:00"
	if p, err = s.life.SetSettings(role, p); err != nil {
		t.Fatal(err)
	}
	now := turn.CreatedAt.Add(continuationIdle + time.Minute)
	if err := s.sweepBackgroundCare(now); err != nil || model.calls.Load() != 1 {
		t.Fatalf("background model called before opt-in: %d, %v", model.calls.Load(), err)
	}
	p.ContinueChats = true
	if _, err := s.life.SetSettings(role, p); err != nil {
		t.Fatal(err)
	}
	if err := s.sweepBackgroundCare(now); err != nil {
		t.Fatal(err)
	}
	sess, err := s.session(session)
	if err != nil {
		t.Fatal(err)
	}
	messages := sess.Messages()
	items, err := s.life.Care(role)
	if err != nil || len(items) != 1 || items[0].Status != "ready" || items[0].SessionID != session || len(messages) != 3 || messages[2].Origin != "proactive" {
		t.Fatalf("continuation did not persist in the shared session: care=%+v messages=%+v err=%v", items, messages, err)
	}
	if err := s.sweepBackgroundCare(now.Add(time.Second)); err != nil || model.calls.Load() != 2 {
		t.Fatalf("continuation was repeated: %d, %v", model.calls.Load(), err)
	}
	if _, err := s.life.OfferChatContinuation(role, session, "later-turn", "这场演出晚点再聊吧", now.Add(25*time.Hour), s.location); !errors.Is(err, companion.ErrCareBlocked) {
		t.Fatalf("similar topic escaped the deduplication window: %v", err)
	}
}
