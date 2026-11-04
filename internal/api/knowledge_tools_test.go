package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"pocket-agent/internal/companion"
	"pocket-agent/internal/memory"
	"pocket-agent/internal/probe"
)

// One fixture flow exercises model-selected knowledge operations, revisioned
// corrections and history barriers without paid calls or a phrase matrix.
func TestKnowledgeManagementLifecycle(t *testing.T) {
	const token = "knowledge-management-fixture-token-over-32"
	var mu sync.Mutex
	var planned map[string]any
	var captured, toolOutput string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []probe.Message `json:"messages"`
			Tools    []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			http.Error(w, "bad fixture", 400)
			return
		}
		b, _ := json.Marshal(body.Messages)
		last := body.Messages[len(body.Messages)-1]
		mu.Lock()
		captured = string(b)
		args := planned
		if last.Role == "tool" {
			toolOutput = last.Content
		}
		mu.Unlock()
		delta := map[string]any{"content": "按实际结果接着聊。"}
		finish := "stop"
		if last.Role == "user" && args != nil {
			available := false
			for _, tool := range body.Tools {
				available = available || tool.Function.Name == "knowledge_manage"
			}
			if !available {
				t.Error("knowledge tool not registered")
			}
			input, _ := json.Marshal(args)
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "manage-knowledge", "type": "function", "function": map[string]string{"name": "knowledge_manage", "arguments": string(input)}}}}
			finish = "tool_calls"
		}
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	defer provider.Close()
	c := probe.DefaultConfig()
	c.BaseURL, c.Model, c.APIKey = provider.URL, "knowledge-fixture", "fixture-only"
	cfg := Config{Agent: c, AccessToken: token, DataDir: t.TempDir()}
	var app *Server
	var srv *httptest.Server
	start := func() {
		var err error
		app, err = New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		srv = httptest.NewServer(app.Handler())
	}
	start()
	t.Cleanup(func() { srv.Close(); app.Close(context.Background()) })
	request := func(method, path string, body any, target any, want int) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			reader = strings.NewReader(string(b))
		}
		r, _ := http.NewRequest(method, srv.URL+path, reader)
		r.Header.Set("Authorization", "Bearer "+token)
		res, err := srv.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, b)
		}
		if target != nil {
			if err := json.Unmarshal(b, target); err != nil {
				t.Fatal(err)
			}
		}
	}
	var session sessionInfo
	request("POST", "/api/sessions", map[string]string{}, &session, 201)
	run := func(text string, args map[string]any, wantError bool) string {
		t.Helper()
		if args != nil {
			if _, ok := args["evidence"]; !ok {
				args["evidence"] = text
			}
		}
		mu.Lock()
		planned = args
		toolOutput = ""
		mu.Unlock()
		body, _ := json.Marshal(map[string]string{"content": text})
		r, _ := http.NewRequest("POST", srv.URL+"/api/sessions/"+session.ID+"/messages", strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer "+token)
		res, err := srv.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		out, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 || !strings.Contains(string(out), "event: done") {
			t.Fatalf("chat failed: %d %s", res.StatusCode, out)
		}
		mu.Lock()
		result, contextText := toolOutput, captured
		mu.Unlock()
		if args != nil && strings.HasPrefix(result, "Error: ") != wantError {
			t.Fatalf("tool error=%v expected=%v: %s", strings.HasPrefix(result, "Error: "), wantError, result)
		}
		if args == nil {
			return contextText
		}
		return result
	}
	fact, err := app.memory.Create("companion", "用户住在甲楼", "approved", "manual", session.ID)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := app.memory.Create("companion", "用户的住址是乙楼", "approved", "manual", session.ID)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := app.memory.Create("companion", "候选喜欢绿茶", "pending", "agent", session.ID)
	if err != nil {
		t.Fatal(err)
	}
	other, err := app.memory.Create("planner", "其他角色住在丙楼", "approved", "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	if text := run("我住在哪里，先聊聊。", nil, false); !strings.Contains(text, "甲楼") {
		t.Fatal("fixture did not establish old history")
	}
	run("我的住址改为乙楼了，以后按这个记。", map[string]any{"operation": "update", "kind": "fact", "id": fact.ID, "revision": fact.Revision, "content": "用户住在乙楼"}, false)
	fact, err = app.memory.Get("companion", fact.ID)
	if err != nil || fact.Revision != 2 || fact.Status != "approved" {
		t.Fatal("fact correction failed")
	}
	if text := run("我住哪里？", nil, false); strings.Contains(text, "甲楼") {
		t.Fatal("old chat resurfaced after correction")
	}
	result := run("找一下住址相关的记录。", map[string]any{"operation": "search", "kind": "fact", "query": "住址"}, false)
	if strings.Contains(result, pending.Content) {
		t.Fatal("candidate leaked into default fact search")
	}
	run("这条候选先改一下。", map[string]any{"operation": "update", "kind": "fact", "id": pending.ID, "revision": pending.Revision, "content": "候选喜欢红茶", "status": "approved"}, true)
	if item, _ := app.memory.Get("companion", pending.ID); item.Status != "pending" || item.Revision != 1 {
		t.Fatal("tool promoted pending fact")
	}
	run("把候选和已确认的记录混在一起试试。", map[string]any{"operation": "merge", "kind": "fact", "references": []memory.Reference{{ID: fact.ID, Revision: fact.Revision}, {ID: pending.ID, Revision: pending.Revision}}, "content": "不能跨审核状态合并"}, true)
	if item, _ := app.memory.Get("companion", pending.ID); item.Status != "pending" || item.Revision != 1 {
		t.Fatal("merge promoted a pending fact")
	}
	refs := []memory.Reference{{ID: fact.ID, Revision: fact.Revision}, {ID: duplicate.ID, Revision: 0}}
	run("把两条住址记录归并一下。", map[string]any{"operation": "merge", "kind": "fact", "references": refs, "content": "用户住在乙楼"}, true)
	refs[1].Revision = duplicate.Revision
	run("两条住址是同一件事，整理成一条吧。", map[string]any{"operation": "merge", "kind": "fact", "references": refs, "content": "用户住在乙楼"}, false)
	fact, _ = app.memory.Get("companion", fact.ID)
	if fact.Revision != 3 || !strings.Contains(fact.Source, "merged:") || !strings.Contains(fact.Source, duplicate.ID) {
		t.Fatal("merge lost identity or source trace")
	}
	if _, err := app.memory.Get("companion", duplicate.ID); err == nil {
		t.Fatal("merge retained redundant record")
	}
	run("把那个住址更正。", map[string]any{"operation": "update", "kind": "fact", "id": other.ID, "revision": other.Revision, "content": "不可跨角色修改"}, true)
	if item, _ := app.memory.Get("planner", other.ID); item.Content != other.Content {
		t.Fatal("cross-role mutation succeeded")
	}
	event, err := app.life.AddEpisode("companion", companion.EpisodeDraft{Title: "周五面试", Content: "地点在甲会场", Status: "open"})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := app.life.AddEpisode("companion", companion.EpisodeDraft{Title: "候选秘密事件", Content: "还没确认", Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	result = run("找找面试安排。", map[string]any{"operation": "search", "kind": "event", "query": "面试安排"}, false)
	if !strings.Contains(result, event.ID) || strings.Contains(result, candidate.ID) {
		t.Fatal("event relevance or default review filter failed")
	}
	run("面试地点改成乙会场了。", map[string]any{"operation": "update", "kind": "event", "id": event.ID, "revision": event.Revision, "content": "地点在乙会场"}, false)
	event, _ = app.life.Episode("companion", event.ID)
	if event.Content != "地点在乙会场" {
		t.Fatal("event correction failed")
	}
	run("试着确认候选。", map[string]any{"operation": "update", "kind": "event", "id": candidate.ID, "revision": candidate.Revision, "status": "open"}, true)
	if item, _ := app.life.Episode("companion", candidate.ID); item.Status != "pending" {
		t.Fatal("tool promoted pending event")
	}
	run("这个面试记录不用保留了。", map[string]any{"operation": "forget", "kind": "event", "id": event.ID, "revision": event.Revision}, false)
	if _, err := app.life.Episode("companion", event.ID); err == nil {
		t.Fatal("event forget failed")
	}
	run("住址记录也请忘掉。", map[string]any{"operation": "forget", "kind": "fact", "id": fact.ID, "revision": fact.Revision}, false)
	if text := run("还有住址记录吗？", nil, false); strings.Contains(text, "乙楼") || strings.Contains(text, "甲楼") {
		t.Fatal("forgotten address came back through context")
	}
	srv.Close()
	if err := app.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	start()
	if _, err := app.memory.Get("companion", fact.ID); err == nil {
		t.Fatal("forgotten fact revived after restart")
	}
	if floor, err := app.life.Floor("companion", session.ID); err != nil || floor == 0 {
		t.Fatal("knowledge history barrier was not durable")
	}
}
