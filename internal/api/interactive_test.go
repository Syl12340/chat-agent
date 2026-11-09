package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"chat-agent/internal/interactive"
	"chat-agent/internal/probe"
	"chat-agent/internal/turns"
)

const choiceFixture = "```chat-ui\n" + `{"schema_version":1,"id":"q1","type":"quiz.single_choice","props":{"question":"Which is larger?","options":[{"id":"A","label":"1"},{"id":"B","label":"2"}]}}` + "\n```"

type uiFeedbackFixture struct {
	t        *testing.T
	calls    atomic.Int32
	feedback atomic.Int32
}

func (p *uiFeedbackFixture) Stream(_ context.Context, input probe.ModelRequest, _ func(probe.Event)) (probe.Message, error) {
	p.calls.Add(1)
	last := input.Messages[len(input.Messages)-1]
	if response, _, _ := interactive.ParseResponse(last.Content); response != nil {
		p.feedback.Add(1)
		for _, tool := range input.Tools {
			if !uiReadOnlyTools[tool.Name] {
				p.t.Errorf("UI answer offered write tool %s", tool.Name)
			}
		}
		if !strings.Contains(input.System, "chat-ui v1") {
			p.t.Error("missing implemented UI guidance")
		}
		return probe.Message{Role: "assistant", Content: "Feedback only."}, nil
	}
	return probe.Message{Role: "assistant", Content: choiceFixture}, nil
}

func TestInteractiveFeedbackLifecycle(t *testing.T) {
	const token = "ui-fixture-token-at-least-32-characters"
	provider := &uiFeedbackFixture{t: t}
	cfg := probe.DefaultConfig()
	cfg.Model = "ecnu-max"
	cfg.Provider = provider
	s, err := New(Config{Agent: cfg, DataDir: t.TempDir(), AccessToken: token})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	request := func(method, path string, body any, want int) *httptest.ResponseRecorder {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w
	}
	newSession := func() string {
		var info sessionInfo
		out := request("POST", "/api/sessions", map[string]string{}, 201)
		json.Unmarshal(out.Body.Bytes(), &info)
		return info.ID
	}
	id, other := newSession(), newSession()
	base := "/api/sessions/" + id
	submit := func(requestID, content string, want int) {
		request("POST", base+"/turns", map[string]string{"request_id": requestID, "content": content, "mode": "auto"}, want)
	}
	type history struct {
		Messages []probe.Message `json:"messages"`
		Turns    []turns.Turn    `json:"turns"`
		Active   bool            `json:"active"`
		Floor    int             `json:"ui_context_floor"`
	}
	read := func() history {
		var h history
		out := request("GET", base+"/messages", nil, 200)
		if err = json.Unmarshal(out.Body.Bytes(), &h); err != nil {
			t.Fatal(err)
		}
		return h
	}
	wait := func(count int) history {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			h := read()
			if !h.Active && len(h.Turns) == count {
				return h
			}
			time.Sleep(15 * time.Millisecond)
		}
		t.Fatal("queue timeout")
		return history{}
	}
	submit("origin", "show a question", 202)
	h := wait(1)
	if h.Turns[0].Status != "completed" || len(h.Messages) != 2 {
		t.Fatal("source did not complete")
	}
	digest := sha256.Sum256([]byte(h.Messages[1].Content))
	response := &interactive.Response{SchemaVersion: 1, ComponentID: "q1", ComponentType: "quiz.single_choice", Source: interactive.Source{MessageIndex: 1, SHA256: hex.EncodeToString(digest[:])}}
	response.Answer.OptionID = "B"
	envelope := func() string {
		raw, _ := json.Marshal(map[string]any{"chat_ui_response": response})
		return string(raw)
	}
	submit("bad-envelope", `{"chat_ui_response":{}}`, 400)
	response.Answer.OptionID = "invented"
	submit("wrong-option", envelope(), 409)
	response.Answer.OptionID = "B"
	response.Source.SHA256 = strings.Repeat("0", 64)
	submit("wrong-hash", envelope(), 409)
	response.Source.SHA256 = hex.EncodeToString(digest[:])
	request("POST", "/api/sessions/"+other+"/turns", map[string]string{"request_id": "other", "content": envelope()}, 409)
	// Legacy streaming must not bypass the typed queue authorization boundary.
	request("POST", base+"/messages", map[string]string{"content": envelope()}, 400)
	request("POST", base+"/cancel", nil, 200)
	answer := envelope()
	submit("answer", answer, 202)
	submit("ordinary", "ordinary text", 202)
	request("POST", base+"/turns/resume", nil, 200)
	h = wait(3)
	if provider.calls.Load() != 3 || provider.feedback.Load() != 1 || h.Turns[1].BatchID == h.Turns[2].BatchID {
		t.Fatal("UI answer merged with ordinary chat or repeated")
	}
	submit("answer", answer, 200)
	if provider.calls.Load() != 3 {
		t.Fatal("idempotent answer called model again")
	}
	// A queued valid source can expire before execution; it must fail before
	// calling the provider, appending input, or acquiring write tools.
	request("POST", base+"/cancel", nil, 200)
	submit("stale-answer", answer, 202)
	if err = s.life.AdvanceCuts("companion", map[string]int{id: len(h.Messages)}); err != nil {
		t.Fatal(err)
	}
	request("POST", base+"/turns/resume", nil, 200)
	after := wait(4)
	if provider.calls.Load() != 3 || after.Turns[3].Status != "failed" || len(after.Messages) != len(h.Messages) || after.Floor != len(h.Messages) {
		t.Fatal("expired queued answer executed or floor missing")
	}
	submit("stale-new", answer, 409)
}
