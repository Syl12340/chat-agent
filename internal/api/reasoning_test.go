package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pocket-agent/internal/probe"
)

type interruptedReasoningFixture struct{ entered chan struct{} }

func (p *interruptedReasoningFixture) Stream(ctx context.Context, r probe.ModelRequest, emit func(probe.Event)) (probe.Message, error) {
	if r.MaxOutputTokens != 0 {
		return probe.Message{}, &probe.ModelError{Kind: "output_limit"}
	}
	emit(probe.Event{Type: probe.EventThinking})
	emit(probe.Event{Type: probe.EventReasoningDelta, Text: "reasoning <script>fixture</script>"})
	close(p.entered)
	<-ctx.Done()
	return probe.Message{}, ctx.Err()
}

func TestInterruptedReasoningSurvivesReopen(t *testing.T) {
	const token = "reasoning-fixture-token-at-least-32"
	p := &interruptedReasoningFixture{entered: make(chan struct{})}
	c := probe.DefaultConfig()
	c.Model, c.Provider = "fixture", p
	config := Config{Agent: c, DataDir: t.TempDir(), AccessToken: token}
	s, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close(context.Background()) }()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code >= 300 {
			t.Fatalf("request failed: %d", w.Code)
		}
		return w
	}
	var session sessionInfo
	if err = json.Unmarshal(request("POST", "/api/sessions", `{"role_id":"companion"}`).Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	base := "/api/sessions/" + session.ID
	request("POST", base+"/turns", `{"request_id":"reasoning-test","content":"fixture"}`)
	select {
	case <-p.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider not entered")
	}
	turn, err := s.turns.Get(session.ID, "reasoning-test")
	if err != nil || turn.Status != "running" || !strings.Contains(turn.ReasoningContent, "reasoning") || turn.Text != "" {
		t.Fatal("live reasoning missing or mixed into answer")
	}
	request("POST", base+"/cancel", "")
	if err = s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	s = reopened
	turn, err = s.turns.Get(session.ID, "reasoning-test")
	if err != nil || turn.Status != "canceled" || turn.ReasoningContent != "reasoning <script>fixture</script>" {
		t.Fatal("canceled reasoning lost after reopen")
	}
	history := request("GET", base+"/messages", "")
	if !strings.Contains(history.Body.String(), "reasoning_content") {
		t.Fatal("reasoning absent from authorized history")
	}
	request("POST", base+"/reset", "")
	turn, err = s.turns.Get(session.ID, "reasoning-test")
	if err != nil || turn.ReasoningContent != "" {
		t.Fatal("reset retained reasoning")
	}
}
