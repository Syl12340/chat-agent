package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type request struct {
	Model     string `json:"model"`
	Stream    bool   `json:"stream"`
	MaxOutput int    `json:"max_tokens"`
	Tools     []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
	Messages []struct {
		Role, Content string
		ToolCallID    string `json:"tool_call_id"`
		ToolCalls     []struct {
			ID string `json:"id"`
		} `json:"tool_calls"`
	} `json:"messages"`
}

func endpoint(t *testing.T, handler func(http.ResponseWriter, *http.Request, request)) (Config, *httptest.Server) {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer fake-p0-key" {
			t.Error("unexpected route, method or authorization")
			w.WriteHeader(400)
			return
		}
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		validTools := len(req.Tools) == 1 && req.Tools[0].Function.Name == "get_current_time"
		finalReply := false
		for _, message := range req.Messages {
			if !strings.HasPrefix(message.Content, RuntimeContextPrefix) {
				continue
			}
			var snapshot RuntimeSnapshot
			if err := json.NewDecoder(strings.NewReader(strings.TrimPrefix(message.Content, RuntimeContextPrefix))).Decode(&snapshot); err != nil {
				t.Errorf("invalid runtime snapshot: %v", err)
				continue
			}
			finalReply = len(req.Tools) == 0 && message.Role == "user" && snapshot.Source == "runtime-context" && !snapshot.ToolsAvailable && snapshot.ResponsesLeft > 0 && (snapshot.CallsLeft == 0 || snapshot.ResponsesLeft == 1) && strings.Contains(message.Content, "No tool calls are available in this step")
		}
		if req.Model != "test-model" || !req.Stream || req.MaxOutput != 0 || (!validTools && !finalReply) {
			t.Errorf("unexpected model, stream, output limit or tools: %+v", req)
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		handler(w, r, req)
	}))
	t.Cleanup(s.Close)
	c := DefaultConfig()
	c.BaseURL, c.APIKey, c.Model = s.URL+"/v1", "fake-p0-key", "test-model"
	c.Timeout = 5 * time.Second
	return c, s
}

func chunk(w http.ResponseWriter, delta any, finish any) {
	b, _ := json.Marshal(map[string]any{"id": "chat-p0", "object": "chat.completion.chunk", "model": "test-model", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
	fmt.Fprintf(w, "data: %s\n\n", b)
	w.(http.Flusher).Flush()
}
func answer(w http.ResponseWriter, text string) {
	chunk(w, map[string]any{"content": text}, nil)
	chunk(w, map[string]any{}, "stop")
	fmt.Fprint(w, "data: [DONE]\n\n")
}
func newRuntime(t *testing.T, c Config, sess *Session) *Agent {
	t.Helper()
	rt, err := New(c, sess)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	return rt
}

func TestStreamingToolLoop(t *testing.T) {
	var requests atomic.Int32
	c, _ := endpoint(t, func(w http.ResponseWriter, _ *http.Request, req request) {
		switch requests.Add(1) {
		case 1:
			// Split arguments across SSE deltas; execute only after complete.
			chunk(w, map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call-clock", "type": "function", "function": map[string]any{"name": "get_current_time", "arguments": "{"}}}}, nil)
			chunk(w, map[string]any{"tool_calls": []any{
				map[string]any{"index": 0, "function": map[string]any{"arguments": "}"}},
				map[string]any{"index": 1, "id": "call-denied", "type": "function", "function": map[string]any{"name": "shell", "arguments": "{}"}},
				map[string]any{"index": 2, "id": "call-budget", "type": "function", "function": map[string]any{"name": "get_current_time", "arguments": "{}"}},
			}}, "tool_calls")
			fmt.Fprint(w, "data: [DONE]\n\n")
		case 2:
			if len(req.Tools) != 0 {
				t.Error("exhausted tool budget did not reserve a tool-free final reply")
			}
			var call, result, denied, budget bool
			for _, m := range req.Messages {
				if m.Role == "assistant" && len(m.ToolCalls) == 3 && m.ToolCalls[0].ID == "call-clock" {
					call = true
				}
				if m.Role == "tool" && m.ToolCallID == "call-clock" {
					if _, err := time.Parse(time.RFC3339, m.Content); err != nil {
						t.Errorf("invalid clock result: %q", m.Content)
					}
					result = true
				}
				denied = denied || m.Role == "tool" && m.ToolCallID == "call-denied" && strings.Contains(m.Content, "not explicitly allowed")
				budget = budget || m.Role == "tool" && m.ToolCallID == "call-budget" && strings.Contains(m.Content, "limit reached")
			}
			if !call || !result || !denied || !budget {
				t.Error("assistant/tool linkage, default denial or tool budget failed")
			}
			answer(w, "Time received.")
		default:
			t.Error("unexpected additional request")
			w.WriteHeader(400)
		}
	})
	c.MaxToolCalls = 1
	dir := t.TempDir()
	sess, err := OpenSession(dir, AgentID, "loop")
	if err != nil {
		t.Fatal(err)
	}
	rt := newRuntime(t, c, sess)
	var text strings.Builder
	var results int
	err = Run(context.Background(), rt, c.Timeout, "What time is it?", func(ev Event) {
		if ev.Type == EventTextDelta {
			text.WriteString(ev.Text)
		}
		if ev.Type == EventToolResult {
			results++
			if ev.Result == nil || results == 1 && ev.Result.Error != "" || results > 1 && ev.Result.Error == "" {
				t.Errorf("tool failed: %+v", ev.Result)
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if text.String() != "Time received." || results != 3 || requests.Load() != 2 {
		t.Fatalf("text=%q results=%d requests=%d", text.String(), results, requests.Load())
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSession(dir, AgentID, "loop")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if len(reopened.messages) != 6 || len(reopened.messages[1].ToolCalls) != 3 || reopened.messages[2].ToolCallID != "call-clock" || reopened.messages[5].Content != "Time received." {
		t.Fatal("tool-call transcript did not survive reopening")
	}
}

func TestSessionResume(t *testing.T) {
	var requests atomic.Int32
	c, _ := endpoint(t, func(w http.ResponseWriter, _ *http.Request, req request) {
		if requests.Add(1) == 2 {
			var user, assistant bool
			for _, m := range req.Messages {
				user = user || m.Role == "user" && m.Content == "Remember blue."
				assistant = assistant || m.Role == "assistant" && m.Content == "Blue saved."
			}
			if !user || !assistant {
				t.Error("reopened runtime did not send persisted history")
			}
		}
		answer(w, "Blue saved.")
	})
	dir := t.TempDir()
	for i := 0; i < 2; i++ {
		// A fresh Session and Agent each time: no reuse of in-memory history.
		sess, err := OpenSession(dir, AgentID, "resume")
		if err != nil {
			t.Fatal(err)
		}
		rt := newRuntime(t, c, sess)
		prompt := "Remember blue."
		if i == 1 {
			prompt = "What did I ask you to remember?"
		}
		var output strings.Builder
		err = Run(context.Background(), rt, c.Timeout, prompt, func(ev Event) {
			if ev.Type == EventTextDelta {
				output.WriteString(ev.Text)
			}
		})
		if output.String() != "Blue saved." {
			t.Fatalf("plain streaming reply missing: %q", output.String())
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := sess.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if requests.Load() != 2 {
		t.Fatal("resume request missing")
	}
	if b, err := os.ReadFile(filepath.Join(dir, AgentID, "resume.jsonl")); err != nil || !strings.Contains(string(b), "Blue saved.") {
		t.Fatalf("history not written: %v", err)
	}
}

func TestCancelStream(t *testing.T) {
	started, disconnected := make(chan struct{}), make(chan struct{})
	c, s := endpoint(t, func(w http.ResponseWriter, r *http.Request, _ request) {
		chunk(w, map[string]any{"content": "Starting"}, nil)
		close(started)
		select {
		case <-r.Context().Done():
			close(disconnected)
		case <-time.After(5 * time.Second):
			t.Error("provider connection did not cancel")
		}
	})
	rt := newRuntime(t, c, NewSession())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, rt, c.Timeout, "slow reply", nil) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		s.CloseClientConnections()
		t.Fatal("stream did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation cause lost: %v", err)
		}
	case <-time.After(2 * time.Second):
		s.CloseClientConnections()
		t.Fatal("run did not stop")
	}
	select {
	case <-disconnected:
	case <-time.After(2 * time.Second):
		s.CloseClientConnections()
		t.Fatal("upstream HTTP connection still active")
	}
}
