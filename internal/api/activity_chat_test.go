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
	"time"

	"pocket-agent/internal/companion"
	"pocket-agent/internal/probe"
)

// One fixture flow covers actual tool execution, explicit authorization, queue
// retry, pause/resume, revision conflicts and durable state without paid calls.
func TestActivityChatControl(t *testing.T) {
	const token = "activity-chat-fixture-token-over-32-characters"
	const save = "这回看到第二章，给咱们留个书签，下回从第三章开始吧。"
	const progress = "读到第二章，下次从第三章继续。"
	const confirmation = "嗯，按这个进度就好。"
	const startActivity = "想和你一起慢慢读本书，今天刚读完序章，下回从第一章接着来，也替咱们留个进度。"
	var mu sync.Mutex
	var captured string
	var exposed bool
	calls := 0
	holdStarted := make(chan struct{}, 1)
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
		available := false
		for _, tool := range body.Tools {
			available = available || tool.Function.Name == "shared_activity_update"
		}
		b, _ := json.Marshal(body.Messages)
		mu.Lock()
		captured, exposed = string(b), available
		calls++
		mu.Unlock()
		last := body.Messages[len(body.Messages)-1]
		// A successful write must refresh the next model request, not just the
		// initial snapshot used by the fake model to choose its revision.
		if last.Role == "tool" && strings.HasPrefix(last.Content, "{") {
			var result struct {
				Revision int `json:"revision"`
				State    struct {
					Revision int `json:"revision"`
				} `json:"activity_state"`
			}
			if err := json.Unmarshal([]byte(last.Content), &result); err != nil {
				t.Error(err)
			}
			if revision := max(result.Revision, result.State.Revision); revision > 0 {
				var reference struct {
					State struct {
						Revision int `json:"revision"`
					} `json:"activity_state"`
				}
				if err := json.Unmarshal(savedRuntimeContext(t, body.Messages), &reference); err != nil {
					t.Error(err)
				}
				if reference.State.Revision != revision {
					t.Errorf("next step has stale activity revision: got %d want %d", reference.State.Revision, revision)
				}
			}
		}
		decision := last.Content
		if last.Role == "user" && decision == confirmation && len(body.Messages) > 1 && body.Messages[len(body.Messages)-2].Role == "user" {
			decision = body.Messages[len(body.Messages)-2].Content
		}
		if last.Role == "user" && last.Content == "hold-fixture" {
			holdStarted <- struct{}{}
			<-r.Context().Done()
			return
		}
		delta := map[string]any{"content": "这件事按你的意思处理了。"}
		finish := "stop"
		decisions := map[string]bool{save: true, startActivity: true, "请暂停这个活动。": true, "请恢复这个活动。": true, "请结束这个活动。": true, "请清空活动进展。": true, "伪造依据测试。": true, "请保存活动进展：旧版本测试。": true}
		if last.Role == "user" && available && decisions[decision] {
			var ref struct {
				State struct {
					Revision int `json:"revision"`
				} `json:"activity_state"`
			}
			if err := json.Unmarshal(savedRuntimeContext(t, body.Messages), &ref); err != nil {
				t.Error(err)
			}
			args := map[string]any{"revision": ref.State.Revision, "evidence": decision}
			switch decision {
			case save:
				args["progress"] = progress
			case "请暂停这个活动。":
				args["status"] = "paused"
			case "请恢复这个活动。":
				args["status"] = "active"
			case "请结束这个活动。":
				args["status"] = "completed"
			case "请清空活动进展。":
				args["progress"] = ""
			case "伪造依据测试。":
				args["progress"], args["evidence"] = "不可保存", "这段不在用户消息中"
			case "请保存活动进展：旧版本测试。":
				args["progress"], args["revision"] = "不可保存", 0
			}
			input, _ := json.Marshal(args)
			toolCalls := []any{map[string]any{"index": 0, "id": "activity-control-0", "type": "function", "function": map[string]string{"name": "shared_activity_update", "arguments": string(input)}}}
			if decision == save {
				toolCalls = append(toolCalls, map[string]any{"index": 1, "id": "activity-control-1", "type": "function", "function": map[string]string{"name": "shared_activity_update", "arguments": string(input)}})
			}
			delta, finish = map[string]any{"tool_calls": toolCalls}, "tool_calls"
		}
		if last.Role == "user" && last.Content == startActivity {
			input, _ := json.Marshal(map[string]any{"action": "create", "title": "一起读书", "kind": "interest", "goal": "按自己的节奏一起读书", "evidence": startActivity})
			input2, _ := json.Marshal(map[string]any{"action": "create", "title": "一起读书", "kind": "interest", "goal": "按自己的节奏一起读书", "evidence": "下回从第一章接着来"})
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "open-activity", "type": "function", "function": map[string]string{"name": "shared_activity_open", "arguments": string(input)}}, map[string]any{"index": 1, "id": "open-activity-again", "type": "function", "function": map[string]string{"name": "shared_activity_open", "arguments": string(input2)}}}}
			finish = "tool_calls"
		}
		if last.Role == "user" && last.Content == "接着咱们原来那本书吧。" {
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "list-activities", "type": "function", "function": map[string]string{"name": "shared_activity_open", "arguments": `{"action":"list"}`}}}}
			finish = "tool_calls"
		}
		if last.Role == "tool" && strings.HasPrefix(last.Content, "[") {
			var list []companion.Activity
			_ = json.Unmarshal([]byte(last.Content), &list)
			for _, item := range list {
				if item.Title == "读一本书" {
					input, _ := json.Marshal(map[string]any{"action": "open", "activity_id": item.ID, "revision": item.Revision, "evidence": "接着咱们原来那本书吧。"})
					delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "resume-existing", "type": "function", "function": map[string]string{"name": "shared_activity_open", "arguments": string(input)}}}}
					finish = "tool_calls"
					break
				}
			}
		}
		if last.Role == "tool" && strings.Contains(last.Content, `"created":true`) {
			var result struct {
				State struct {
					Revision int `json:"revision"`
				} `json:"activity_state"`
			}
			_ = json.Unmarshal([]byte(last.Content), &result)
			input, _ := json.Marshal(map[string]any{"revision": result.State.Revision, "progress": "读完序章，下回从第一章继续。", "evidence": startActivity})
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "save-new-activity", "type": "function", "function": map[string]string{"name": "shared_activity_update", "arguments": string(input)}}}}
			finish = "tool_calls"
		}
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	defer provider.Close()
	c := probe.DefaultConfig()
	c.BaseURL, c.Model, c.APIKey = provider.URL, "activity-control-fixture", "fixture-only"
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
	createSession := func(role string) sessionInfo {
		var item sessionInfo
		request("POST", "/api/sessions", map[string]string{"role_id": role}, &item, 201)
		return item
	}
	var activity companion.Activity
	request("POST", "/api/activities", companion.ActivityDraft{Kind: "interest", Title: "读一本书", Goal: "慢慢读，不催促", IdempotencyKey: "chat-control-activity"}, &activity, 201)
	session := createSession("companion")
	base := "/api/sessions/" + session.ID
	request("POST", "/api/activities/"+activity.ID+"/attach", map[string]any{"revision": activity.Revision, "session_id": session.ID}, nil, 200)
	read := func() companion.Activity {
		a, err := app.activities.Get("companion", activity.ID)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	chat := func(id, text string) {
		t.Helper()
		req, _ := http.NewRequest("POST", srv.URL+"/api/sessions/"+id+"/messages", strings.NewReader(fmt.Sprintf(`{"content":%s}`, func() string { b, _ := json.Marshal(text); return string(b) }())))
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 || !strings.Contains(string(b), "event: done") {
			t.Fatalf("chat: %d %s", res.StatusCode, b)
		}
	}
	chat(session.ID, "今天有点累，想聊两句。")
	mu.Lock()
	hasTool := exposed
	mu.Unlock()
	if !hasTool || read().Revision != 1 {
		t.Fatal("model should choose a direct reply while tools remain registered")
	}
	chat(session.ID, "不要保存活动进展，今天只是聊聊。")
	mu.Lock()
	hasTool = exposed
	mu.Unlock()
	if !hasTool || read().Revision != 1 {
		t.Fatal("negative request authorized mutation")
	}
	turn := map[string]string{"request_id": "save-activity-once", "content": save, "mode": "auto"}
	mu.Lock()
	before := calls
	mu.Unlock()
	request("POST", base+"/cancel", nil, nil, 200)
	request("POST", base+"/turns", turn, nil, 202)
	request("POST", base+"/turns", turn, nil, 200)
	request("POST", base+"/turns", map[string]string{"request_id": "confirm-progress", "content": confirmation, "mode": "auto"}, nil, 202)
	request("POST", base+"/turns/resume", nil, nil, 200)
	var history struct {
		Active   bool                `json:"active"`
		Activity *companion.Activity `json:"activity"`
		Messages []probe.Message     `json:"messages"`
	}
	for i := 0; i < 120; i++ {
		request("GET", base+"/messages", nil, &history, 200)
		if !history.Active && history.Activity != nil && history.Activity.Revision == 2 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if read().Progress != progress || read().Revision != 2 || history.Active {
		t.Fatalf("chat tool did not save exactly once: revision=%d active=%v", read().Revision, history.Active)
	}
	for _, message := range history.Messages {
		if strings.HasPrefix(message.Content, probe.RuntimeContextPrefix) {
			t.Fatal("ephemeral runtime snapshot persisted as chat")
		}
	}
	mu.Lock()
	after := calls
	mu.Unlock()
	if after-before != 2 {
		t.Fatalf("queue retry replayed model request: %d", after-before)
	}
	request("POST", base+"/turns", turn, nil, 200)
	if read().Revision != 2 {
		t.Fatal("completed retry reapplied mutation")
	}
	chat(session.ID, "伪造依据测试。")
	if read().Revision != 2 {
		t.Fatal("fabricated user evidence accepted")
	}
	chat(session.ID, "请保存活动进展：旧版本测试。")
	if read().Revision != 2 {
		t.Fatal("stale revision accepted")
	}
	chat(session.ID, "请暂停这个活动。")
	if read().Status != "paused" {
		t.Fatal("explicit pause not saved")
	}
	chat(session.ID, "我们先聊点别的。")
	mu.Lock()
	text, hasTool := captured, exposed
	mu.Unlock()
	if !hasTool || strings.Contains(text, progress) {
		t.Fatal("paused activity progress leaked or registry depended on query")
	}
	chat(session.ID, "请恢复这个活动。")
	if read().Status != "active" || read().Progress != progress {
		t.Fatal("resume lost saved progress")
	}
	chat(session.ID, "请清空活动进展。")
	if read().Progress != "" {
		t.Fatal("explicit clear not saved")
	}
	ordinary := createSession("companion")
	chat(ordinary.ID, save)
	mu.Lock()
	hasTool = exposed
	mu.Unlock()
	if !hasTool || read().Progress != "" {
		t.Fatal("unbound update must fail without hiding the tool")
	}
	chat(ordinary.ID, startActivity)
	opened, err := app.activities.Session("companion", ordinary.ID)
	if err != nil || opened == nil || opened.ID == activity.ID || opened.Revision != 2 || !strings.Contains(opened.Progress, "序章") {
		t.Fatal("model could not create and update an activity in one bounded tool loop")
	}
	items, _ := app.activities.List("companion")
	if len(items) != 2 {
		t.Fatal("repeated open with another evidence quote duplicated activity")
	}
	chat(ordinary.ID, "接着咱们原来那本书吧。")
	rebound, err := app.activities.Session("companion", ordinary.ID)
	if err != nil || rebound == nil || rebound.ID != activity.ID {
		t.Fatal("model list/open loop did not switch activity atomically")
	}
	if !strings.Contains(text, "System tools and autonomous decisions") || !strings.Contains(text, "shared_activity_open") {
		t.Fatal("system tool guide missing")
	}
	planner := createSession("planner")
	chat(planner.ID, save)
	mu.Lock()
	hasTool = exposed
	mu.Unlock()
	if !hasTool || read().Progress != "" {
		t.Fatal("role scope must hold even with a common tool registry")
	}
	otherDone := make(chan struct{})
	go func() {
		defer close(otherDone)
		req, _ := http.NewRequest("POST", srv.URL+"/api/sessions/"+ordinary.ID+"/messages", strings.NewReader(`{"content":"hold-fixture"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := srv.Client().Do(req)
		if err == nil {
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
		}
	}()
	select {
	case <-holdStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("parallel fixture did not start")
	}
	revision := read().Revision
	chat(session.ID, save)
	if read().Revision != revision || read().Progress != "" {
		t.Fatal("tool changed activity during another role conversation")
	}
	request("POST", "/api/sessions/"+ordinary.ID+"/cancel", nil, nil, 200)
	select {
	case <-otherDone:
	case <-time.After(3 * time.Second):
		t.Fatal("parallel fixture did not stop")
	}
	chat(session.ID, "请结束这个活动。")
	if read().Status != "completed" {
		t.Fatal("completion not saved")
	}
	srv.Close()
	if err := app.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	start()
	if read().Status != "completed" || read().Progress != "" {
		t.Fatal("restart lost chat controls")
	}
}
