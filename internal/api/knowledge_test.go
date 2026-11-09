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
	"sync/atomic"
	"testing"
	"time"

	"chat-agent/internal/memory"
	"chat-agent/internal/probe"
)

func TestRoleMemoryLifecycle(t *testing.T) {
	var expected atomic.Value
	expected.Store("")
	var expectedRole atomic.Value
	expectedRole.Store("小伴")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []probe.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if !strings.Contains(req.Messages[0].Content, expectedRole.Load().(string)) {
			t.Error("wrong role prompt")
		}
		last := req.Messages[len(req.Messages)-1]
		send := func(delta any, finish any) {
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
			fmt.Fprintf(w, "data: %s\n\n", b)
			w.(http.Flusher).Flush()
		}
		tool := func(name, input string) {
			send(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call-p2-" + name, "type": "function", "function": map[string]string{"name": name, "arguments": input}}}}, "tool_calls")
			fmt.Fprint(w, "data: [DONE]\n\n")
		}
		if last.Role == "tool" {
			if last.ToolCallID == "call-p2-memory_search" {
				want := expected.Load().(string)
				if want == "" && last.Content != "[]" {
					t.Errorf("pending or other-role memory leaked: %s", last.Content)
				}
				if want != "" && !strings.Contains(last.Content, want) {
					t.Errorf("approved/corrected memory not returned: %s", last.Content)
				}
			}
			if last.ToolCallID == "call-p2-memory_write" && !strings.Contains(last.Content, `"status":"pending"`) {
				t.Error("model write bypassed review")
			}
			if last.ToolCallID == "call-p2-skill_load" && !strings.Contains(last.Content, "记忆整理") {
				t.Error("role skill not loaded")
			}
			send(map[string]string{"content": "Verified."}, "stop")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		switch last.Content {
		case "提议记忆":
			tool("memory_write", `{"content":"用户喜欢乌龙茶"}`)
		case "加载记忆整理技能":
			tool("skill_load", `{"name":"memory-review"}`)
		default:
			want := expected.Load().(string)
			hasRef := false
			var reference struct {
				Approved []memory.Entry `json:"approved_memories"`
			}
			if err := json.Unmarshal(savedRuntimeContext(t, req.Messages), &reference); err != nil {
				t.Error(err)
			}
			hasRef = len(reference.Approved) > 0
			data, _ := json.Marshal(reference.Approved)
			if (want == "" && hasRef) || (want != "" && !strings.Contains(string(data), want)) {
				t.Error("incorrect automatic role memory reference")
			}
			if want != "" && !hasRef {
				t.Error("approved memory not recalled across sessions")
			}
			tool("memory_search", `{"query":"喜欢"}`)
		}
	}))
	defer upstream.Close()
	c := probe.DefaultConfig()
	c.BaseURL = upstream.URL + "/v1"
	c.Model = "fake"
	c.APIKey = "fake"
	cfg := Config{Agent: c, DataDir: t.TempDir(), AccessToken: "test-role-memory-access-token-32-characters"}
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
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, resp.StatusCode, want, data)
		}
		return data
	}
	create := func(role string) string {
		if role != "companion" {
			return legacySession(t, app, role)
		}
		var info sessionInfo
		_ = json.Unmarshal(do("POST", "/api/sessions", map[string]string{"role_id": role}, 201), &info)
		return info.ID
	}
	chat := func(id, text string) {
		data := do("POST", "/api/sessions/"+id+"/messages", map[string]string{"content": text}, 200)
		if !strings.Contains(string(data), "event: done\n") {
			t.Fatalf("chat did not complete: %s", data)
		}
	}
	list := func(role string) []memory.Entry {
		var entries []memory.Entry
		_ = json.Unmarshal(do("GET", "/api/memory?role_id="+role, nil, 200), &entries)
		return entries
	}
	if roles := do("GET", "/api/roles", nil, 200); !strings.Contains(string(roles), "companion") || !strings.Contains(string(roles), "planner") {
		t.Fatal("default roles missing")
	}
	do("POST", "/api/sessions", map[string]string{"role_id": "planner"}, 400)
	companion := create("companion")
	chat(companion, "提议记忆")
	entries := list("companion")
	if len(entries) != 1 || entries[0].Status != "pending" {
		t.Fatal("candidate not isolated as pending")
	}
	entry := entries[0]
	chat(create("companion"), "核对我喜欢什么")
	_ = json.Unmarshal(do("PATCH", "/api/memory/"+entry.ID+"?role_id=companion", map[string]any{"status": "approved", "revision": entry.Revision}, 200), &entry)
	expected.Store("用户喜欢乌龙茶")
	chat(create("companion"), "核对我喜欢什么")
	expected.Store("")
	expectedRole.Store("计划伙伴")
	planner := create("planner")
	chat(planner, "核对我喜欢什么")
	if len(list("planner")) != 0 {
		t.Fatal("role memory leaked")
	}
	do("PATCH", "/api/memory/"+entry.ID+"?role_id=planner", map[string]any{"content": "wrong role", "revision": entry.Revision}, 404)
	expectedRole.Store("小伴")
	_ = json.Unmarshal(do("PATCH", "/api/memory/"+entry.ID+"?role_id=companion", map[string]any{"content": "用户喜欢红茶", "revision": entry.Revision}, 200), &entry)
	expected.Store("用户喜欢红茶")
	chat(create("companion"), "核对我喜欢什么")
	do("PATCH", "/api/memory/"+entry.ID+"?role_id=companion", map[string]any{"content": "stale overwrite", "revision": entry.Revision - 1}, 409)
	chat(companion, "加载记忆整理技能")
	export := do("GET", "/api/memory/export?role_id=companion", nil, 200)
	if !strings.Contains(string(export), "红茶") || strings.Contains(string(export), "乌龙茶") {
		t.Fatal("memory export contains outdated facts")
	}
	do("PATCH", "/api/roles/companion", map[string]string{"file": "PROFILE.md", "content": "# 自定义风格\n\n保持简明，先回应问题。"}, 200)
	do("PATCH", "/api/roles/companion", map[string]string{"file": "../outside.md", "content": "escape"}, 400)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := app.Close(ctx); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	app, srv = start()
	if entries := list("companion"); len(entries) != 1 || entries[0].Content != "用户喜欢红茶" {
		t.Fatal("approved memory not restored")
	}
	if data := do("GET", "/api/sessions/"+planner+"/messages", nil, 200); !strings.Contains(string(data), `"role_id":"planner"`) {
		t.Fatal("session role not restored")
	}
	if data := do("GET", "/api/roles/companion", nil, 200); !strings.Contains(string(data), "自定义风格") {
		t.Fatal("role update not restored")
	}
	do("DELETE", "/api/memory/"+entry.ID+"?role_id=companion&revision="+fmt.Sprint(entry.Revision), nil, 200)
	if len(list("companion")) != 0 {
		t.Fatal("memory not deleted")
	}
	data, err := os.ReadFile(filepath.Join(cfg.DataDir, "memory", "companion.json"))
	if err != nil || strings.Contains(string(data), "红茶") {
		t.Fatal("deleted memory remains in current memory file")
	}
	expected.Store("")
	chat(create("companion"), "核对我喜欢什么")
}

// Read the actual model-bound host snapshot, not the retired flat reference
// message. Shared by the memory and activity lifecycle fixtures.
func savedRuntimeContext(t *testing.T, messages []probe.Message) json.RawMessage {
	t.Helper()
	var saved json.RawMessage
	count := 0
	for _, message := range messages {
		if !strings.HasPrefix(message.Content, probe.RuntimeContextPrefix) {
			continue
		}
		count++
		var snapshot probe.RuntimeSnapshot
		if err := json.NewDecoder(strings.NewReader(strings.TrimPrefix(message.Content, probe.RuntimeContextPrefix))).Decode(&snapshot); err != nil {
			t.Errorf("invalid model-bound runtime snapshot: %v", err)
			continue
		}
		if message.Role != "user" || snapshot.Source != "runtime-context" {
			t.Error("runtime snapshot lost its source")
		}
		var reference struct {
			Saved json.RawMessage `json:"saved_context"`
		}
		if err := json.Unmarshal(snapshot.Reference, &reference); err != nil {
			t.Error(err)
			continue
		}
		saved = reference.Saved
	}
	if count != 1 || len(saved) == 0 {
		t.Errorf("expected one model-bound saved-context snapshot, got %d", count)
		return json.RawMessage(`{}`)
	}
	return saved
}
