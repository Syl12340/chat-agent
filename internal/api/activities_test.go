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

	"chat-agent/internal/companion"
	"chat-agent/internal/probe"
)

// One fixture flow checks explicit binding, revisioned progress and its history
// barrier, consent to pause/end, role scope and durable deletion without quota.
func TestSharedActivityLifecycle(t *testing.T) {
	const token = "shared-activity-fixture-token-over-32-characters"
	var mu sync.Mutex
	lastContext := ""
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		lastContext = string(b)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"这件事我们慢慢聊。\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	c := probe.DefaultConfig()
	c.BaseURL, c.Model, c.APIKey = provider.URL, "activity-fixture", "fixture-only"
	cfg := Config{Agent: c, DataDir: t.TempDir(), AccessToken: token}
	var app *Server
	var srv *httptest.Server
	start := func() {
		t.Helper()
		var err error
		app, err = New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		srv = httptest.NewServer(app.Handler())
	}
	start()
	t.Cleanup(func() { srv.Close(); app.Close(context.Background()) })
	request := func(method, path string, body any, auth bool) (int, []byte) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			reader = strings.NewReader(string(b))
		}
		req, _ := http.NewRequest(method, srv.URL+path, reader)
		if auth {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, b
	}
	decode := func(method, path string, body, target any, want int) {
		t.Helper()
		code, b := request(method, path, body, true)
		if code != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, code, want, b)
		}
		if target != nil {
			if err := json.Unmarshal(b, target); err != nil {
				t.Fatal(err)
			}
		}
	}
	newSession := func(role string) sessionInfo {
		t.Helper()
		var session sessionInfo
		decode("POST", "/api/sessions", map[string]string{"role_id": role}, &session, 201)
		return session
	}
	chat := func(session sessionInfo, content string) string {
		t.Helper()
		code, reply := request("POST", "/api/sessions/"+session.ID+"/messages", map[string]string{"content": content}, true)
		if code != 200 || !strings.Contains(string(reply), "event: done") {
			t.Fatalf("fixture chat: %d %s", code, reply)
		}
		mu.Lock()
		defer mu.Unlock()
		return lastContext
	}
	if code, _ := request("GET", "/api/activities", nil, false); code != 401 {
		t.Fatal("activities not authenticated")
	}
	draft := companion.ActivityDraft{Kind: "bedtime", Title: "睡前聊一点", Goal: "甲楼旧目标", IdempotencyKey: "bedtime-fixture-01"}
	var activity, duplicate companion.Activity
	decode("POST", "/api/activities", draft, &activity, 201)
	decode("POST", "/api/activities", draft, &duplicate, 200)
	if duplicate.ID != activity.ID {
		t.Fatal("create retry duplicated activity")
	}
	conflict := draft
	conflict.Title = "另一件事"
	decode("POST", "/api/activities", conflict, nil, 409)
	var planner companion.Activity
	decode("POST", "/api/activities?role_id=planner", companion.ActivityDraft{Kind: "small_step", Title: "独立的一小步", IdempotencyKey: "planner-fixture-01"}, &planner, 201)
	var scoped []companion.Activity
	decode("GET", "/api/activities?role_id=planner", nil, &scoped, 200)
	if len(scoped) != 1 || scoped[0].ID != planner.ID {
		t.Fatal("activity crossed roles")
	}
	bound := newSession("companion")
	attachPath := "/api/activities/" + activity.ID + "/attach"
	attachment := map[string]any{"revision": activity.Revision, "session_id": bound.ID}
	decode("POST", attachPath+"?role_id=planner", attachment, nil, 409)
	decode("POST", attachPath, attachment, nil, 200)
	decode("POST", attachPath, attachment, nil, 200)
	if context := chat(bound, "刚才说到甲楼旧目标，先聊一会儿"); !strings.Contains(context, "shared_activity") || !strings.Contains(context, "bedtime") || !strings.Contains(context, "甲楼旧目标") {
		t.Fatal("bound activity missing from model context")
	}
	decode("POST", attachPath, attachment, nil, 409)
	progress := "现在改为乙楼进展"
	decode("PATCH", "/api/activities/"+activity.ID, map[string]any{"revision": activity.Revision, "goal": "新的乙楼目标", "progress": progress}, &activity, 200)
	floor, err := app.activities.Floor("companion", bound.ID)
	if err != nil || floor < 2 {
		t.Fatal("progress change did not persist history barrier")
	}
	if context := chat(bound, "接着聊吧"); strings.Contains(context, "甲楼旧目标") || !strings.Contains(context, progress) {
		t.Fatal("outdated progress re-entered model context")
	}
	decode("PATCH", "/api/activities/"+activity.ID, map[string]any{"revision": activity.Revision - 1, "progress": "过时页面覆盖"}, nil, 409)
	ordinary := newSession("companion")
	if context := chat(ordinary, "随便聊聊"); strings.Contains(context, progress) || strings.Contains(context, "新的乙楼目标") {
		t.Fatal("unbound conversation inherited an activity")
	}
	decode("PATCH", "/api/activities/"+activity.ID, map[string]any{"revision": activity.Revision, "status": "paused"}, &activity, 200)
	if context := chat(bound, "今天先不聊活动"); strings.Contains(context, progress) || strings.Contains(context, "新的乙楼目标") {
		t.Fatal("paused activity still entered model context")
	}
	if a, err := app.activities.Session("companion", bound.ID); err != nil || a == nil || a.Status != "paused" {
		t.Fatal("paused activity not available to its management UI")
	}
	decode("POST", attachPath, map[string]any{"revision": activity.Revision, "session_id": ordinary.ID}, nil, 409)
	decode("PATCH", "/api/activities/"+activity.ID, map[string]any{"revision": activity.Revision, "status": "active"}, &activity, 200)
	continued := newSession("companion")
	decode("POST", attachPath, map[string]any{"revision": activity.Revision, "session_id": continued.ID}, nil, 200)
	if context := chat(continued, "从保存的进展接着聊"); !strings.Contains(context, progress) {
		t.Fatal("new session did not recover saved progress")
	}
	if a, err := app.activities.Context("planner", continued.ID); err != nil || a != nil {
		t.Fatal("bound activity leaked across roles")
	}
	decode("PATCH", "/api/activities/"+activity.ID, map[string]any{"revision": activity.Revision, "status": "completed"}, &activity, 200)
	if context := chat(continued, "换个话题"); strings.Contains(context, progress) || strings.Contains(context, "新的乙楼目标") {
		t.Fatal("completed activity still entered model context")
	}
	decode("DELETE", fmt.Sprintf("/api/activities/%s?revision=%d", activity.ID, activity.Revision-1), nil, nil, 409)
	decode("DELETE", fmt.Sprintf("/api/activities/%s?revision=%d", activity.ID, activity.Revision), nil, nil, 200)
	decode("POST", "/api/activities", draft, nil, 409)
	if a, err := app.activities.Context("companion", continued.ID); err != nil || a != nil {
		t.Fatal("deleted activity remained in context")
	}
	srv.Close()
	if err := app.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	start()
	decode("GET", "/api/activities", nil, &scoped, 200)
	if len(scoped) != 0 {
		t.Fatal("deleted activity returned after restart")
	}
	decode("POST", "/api/activities", draft, nil, 409)
	floor, err = app.activities.Floor("companion", continued.ID)
	if err != nil || floor < 2 {
		t.Fatal("deleted activity history barrier lost on restart")
	}
	decode("POST", "/api/sessions/"+continued.ID+"/reset", nil, nil, 200)
	if floor, err := app.activities.Floor("companion", continued.ID); err != nil || floor != 0 {
		t.Fatal("session reset did not clear activity history barrier")
	}
	decode("GET", "/api/activities?role_id=planner", nil, &scoped, 200)
	if len(scoped) != 1 || scoped[0].ID != planner.ID {
		t.Fatal("other role activity changed during deletion/restart")
	}
}
