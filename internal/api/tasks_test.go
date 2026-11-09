package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"chat-agent/internal/probe"
	"chat-agent/internal/tasks"
)

// One P3 lifecycle combines tool execution, API edits, deduplication, durable
// due-state transitions, write-failure recovery and restarting the application.
func TestTasksAndRemindersLifecycle(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []probe.Message `json:"messages"`
			Tools    []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		for _, tool := range req.Tools {
			if strings.HasPrefix(tool.Function.Name, "web_") {
				t.Error("web tools enabled without configuration")
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(delta any, reason string) {
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": reason}}})
			fmt.Fprintf(w, "data: %s\n\n", b)
			w.(http.Flusher).Flush()
		}
		if req.Messages[len(req.Messages)-1].Role == "tool" {
			ids := []string{}
			for _, m := range req.Messages {
				if m.Role == "tool" {
					var item tasks.Item
					var settled struct {
						Item *tasks.Item `json:"item"`
					}
					if json.Unmarshal([]byte(m.Content), &item) != nil || item.ID == "" {
						_ = json.Unmarshal([]byte(m.Content), &settled)
						if settled.Item != nil {
							item = *settled.Item
						}
					}
					if item.ID == "" {
						t.Errorf("task tool failed: %s", m.Content)
					}
					ids = append(ids, item.ID)
				}
			}
			if len(ids) != 2 || ids[0] != ids[1] {
				t.Error("repeated tool calls created duplicate todos")
			}
			send(map[string]string{"content": "Saved once."}, "stop")
		} else {
			calls := []any{}
			for _, id := range []string{"task-one", "task-retry"} {
				calls = append(calls, map[string]any{"index": len(calls), "id": id, "type": "function", "function": map[string]string{"name": "todo_add", "arguments": `{"title":"整理项目笔记"}`}})
			}
			send(map[string]any{"tool_calls": calls}, "tool_calls")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	c := probe.DefaultConfig()
	c.BaseURL = upstream.URL + "/v1"
	c.Model = "fake"
	c.APIKey = "fake"
	cfg := Config{Agent: c, DataDir: t.TempDir(), AccessToken: "test-p3-access-token-over-thirty-two-characters"}
	start := func() (*Server, *httptest.Server) {
		app, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(app.Handler())
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			app.Close(ctx)
			srv.Close()
		})
		return app, srv
	}
	app, srv := start()
	client := &http.Client{Timeout: 5 * time.Second}
	do := func(method, path string, body any, want int) []byte {
		t.Helper()
		var input io.Reader
		if body != nil {
			data, _ := json.Marshal(body)
			input = strings.NewReader(string(data))
		}
		req, _ := http.NewRequest(method, srv.URL+path, input)
		req.Header.Set("Authorization", "Bearer "+cfg.AccessToken)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s: %d want %d: %s", path, resp.StatusCode, want, data)
		}
		return data
	}
	var session sessionInfo
	_ = json.Unmarshal(do("POST", "/api/sessions", nil, 201), &session)
	stream := do("POST", "/api/sessions/"+session.ID+"/messages", map[string]string{"content": "添加待办"}, 200)
	if !strings.Contains(string(stream), "event: tool_result") || !strings.Contains(string(stream), "event: done") {
		t.Fatal("tool execution not visible")
	}
	var items []tasks.Item
	_ = json.Unmarshal(do("GET", "/api/todos?role_id=companion", nil, 200), &items)
	if len(items) != 1 {
		t.Fatal("duplicate todo")
	}
	var manual tasks.Item
	input := map[string]string{"title": "核对草稿", "idempotency_key": "manual-todo-key"}
	_ = json.Unmarshal(do("POST", "/api/todos?role_id=companion", input, 201), &manual)
	input["idempotency_key"] = "alias-todo-key"
	var alias tasks.Item
	_ = json.Unmarshal(do("POST", "/api/todos?role_id=companion", input, 201), &alias)
	if alias.ID != manual.ID {
		t.Fatal("semantic duplicate not deduplicated")
	}
	_ = json.Unmarshal(do("PATCH", "/api/todos/"+manual.ID+"?role_id=companion", map[string]any{"status": "done", "revision": manual.Revision}, 200), &manual)
	do("PATCH", "/api/todos/"+manual.ID+"?role_id=companion", map[string]any{"status": "open", "revision": manual.Revision - 1}, 409)
	do("PATCH", "/api/todos/"+manual.ID+"?role_id=planner", map[string]any{"status": "open", "revision": manual.Revision}, 404)
	_ = json.Unmarshal(do("POST", "/api/todos?role_id=companion", input, 201), &alias)
	if alias.ID != manual.ID || alias.Status != "done" {
		t.Fatal("retry key was lost after completion")
	}
	do("POST", "/api/todos?role_id=companion", map[string]string{"title": "different content", "idempotency_key": "alias-todo-key"}, 409)
	do("POST", "/api/todos?role_id=companion", map[string]string{"title": "整理项目笔记", "idempotency_key": "alias-todo-key"}, 409)
	if data := do("GET", "/api/todos?role_id=planner", nil, 200); string(data) != "[]\n" {
		t.Fatal("todo role isolation failed")
	}
	now := time.Now().UTC()
	var reminder tasks.Item
	_ = json.Unmarshal(do("POST", "/api/reminders?role_id=companion", map[string]any{"title": "喝水", "due_at": now.Add(-5 * time.Second), "idempotency_key": "due-reminder-key"}, 201), &reminder)
	if _, err := app.tasks.Advance(now); err != nil {
		t.Fatal(err)
	}
	reminders, err := app.tasks.List("companion", "reminder")
	if err != nil || len(reminders) != 1 || reminders[0].Status != "due" || reminders[0].FiredAt == nil {
		t.Fatal("reminder not moved to inbox")
	}
	reminder = reminders[0]
	if n, err := app.tasks.Advance(now.Add(time.Second)); err != nil || n != 0 {
		t.Fatal("reminder triggered twice")
	}
	_ = json.Unmarshal(do("PATCH", "/api/reminders/"+reminder.ID+"?role_id=companion", map[string]any{"status": "acknowledged", "revision": reminder.Revision}, 200), &reminder)
	var canceled tasks.Item
	_ = json.Unmarshal(do("POST", "/api/reminders?role_id=companion", map[string]any{"title": "不应触发", "due_at": now.Add(time.Minute), "idempotency_key": "cancel-reminder-key"}, 201), &canceled)
	do("PATCH", "/api/reminders/"+canceled.ID+"?role_id=companion", map[string]any{"status": "canceled", "revision": canceled.Revision}, 200)
	var recoverable tasks.Item
	_ = json.Unmarshal(do("POST", "/api/reminders?role_id=companion", map[string]any{"title": "写入恢复检查", "due_at": now.Add(2 * time.Minute), "idempotency_key": "recover-reminder-key"}, 201), &recoverable)
	file := filepath.Join(cfg.DataDir, "tasks", "state.json")
	backup := file + ".saved"
	if err := os.Rename(file, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(file, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := app.tasks.Advance(now.Add(3 * time.Minute)); err == nil {
		t.Fatal("expected failed snapshot write")
	}
	reminders, _ = app.tasks.List("companion", "reminder")
	for _, item := range reminders {
		if item.ID == recoverable.ID && item.Status != "scheduled" {
			t.Fatal("failed write was published")
		}
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, file); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := app.Close(ctx); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	app, srv = start()
	if n, err := app.tasks.Advance(now.Add(3 * time.Minute)); err != nil || n != 1 {
		t.Fatalf("overdue recovery: %d %v", n, err)
	}
	if n, err := app.tasks.Advance(now.Add(4 * time.Minute)); err != nil || n != 0 {
		t.Fatal("recovered reminder fired more than once")
	}
	reminders, _ = app.tasks.List("companion", "reminder")
	for _, item := range reminders {
		if item.ID == reminder.ID && item.Status != "acknowledged" || item.ID == canceled.ID && item.Status != "canceled" {
			t.Fatal("ack/cancel state lost on restart")
		}
	}
	_ = json.Unmarshal(do("POST", "/api/todos?role_id=companion", input, 201), &alias)
	if alias.ID != manual.ID {
		t.Fatal("idempotency alias not restored")
	}
}
