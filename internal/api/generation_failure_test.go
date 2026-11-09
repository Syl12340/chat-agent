package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"chat-agent/internal/probe"
	"chat-agent/internal/turns"
)

type recoverableFailureFixture struct{ calls atomic.Int32 }

func (p *recoverableFailureFixture) Stream(context.Context, probe.ModelRequest, func(probe.Event)) (probe.Message, error) {
	if p.calls.Add(1) == 1 {
		return probe.Message{}, &probe.ModelError{Kind: "http", HTTPStatus: 429}
	}
	return probe.Message{Role: "assistant", Content: "fixture reply"}, nil
}

func TestFailureReasonAndEmptyQueueRecovery(t *testing.T) {
	const token = "failure-fixture-token-at-least-32"
	p := &recoverableFailureFixture{}
	c := probe.DefaultConfig()
	c.Model = "fixture"
	c.Provider = p
	s, err := New(Config{Agent: c, DataDir: t.TempDir(), AccessToken: token})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	request := func(method, path string, body any, want int) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w
	}
	var session sessionInfo
	json.Unmarshal(request("POST", "/api/sessions", map[string]string{}, 201).Body.Bytes(), &session)
	base := "/api/sessions/" + session.ID
	wait := func(id, status string) turns.Turn {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			turn, e := s.turns.Get(session.ID, id)
			s.mu.Lock()
			busy := s.runs[session.ID] != nil
			s.mu.Unlock()
			if e == nil && turn.Status == status && !busy {
				return turn
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("turn did not settle")
		return turns.Turn{}
	}
	request("POST", base+"/turns", map[string]string{"request_id": "failed", "content": "fixture one"}, 202)
	failed := wait("failed", "failed")
	if !strings.Contains(failed.Error, "HTTP 429") {
		t.Fatal("specific failure missing")
	}
	_, paused, err := s.turns.List(session.ID)
	if err != nil || !paused {
		t.Fatal("failure did not pause queue")
	}
	request("POST", base+"/turns/resume", nil, 200)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		busy := s.runs[session.ID] != nil
		s.mu.Unlock()
		if !busy {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if p.calls.Load() != 1 {
		t.Fatal("empty recovery replayed failed request")
	}
	request("POST", base+"/turns", map[string]string{"request_id": "fresh", "content": "fixture two"}, 202)
	wait("fresh", "completed")
	if p.calls.Load() != 2 {
		t.Fatal("fresh message did not run once")
	}
	for _, problem := range []error{context.DeadlineExceeded, &probe.ModelError{Kind: "output_limit"}, &probe.ModelError{Kind: "empty"}, &probe.ModelError{Kind: "stream"}, &probe.ModelError{Kind: "http", HTTPStatus: 401}} {
		code, text := generationFailure(problem)
		if code == "generation_failed" || text == "" || strings.Contains(text, "private") {
			t.Fatal("missing safe error category")
		}
	}
}
