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

// One integrated flow covers consent, edits, role isolation, quiet hours,
// reserved budget and restart recovery without any external model calls.
func TestLifeAndCareLifecycle(t *testing.T) {
	const token = "life-integration-test-token-over-32-characters"
	var providerMu sync.Mutex
	calls := 0
	lastContext := ""
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		providerMu.Lock()
		calls++
		lastContext = string(b)
		providerMu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"这件事进展怎么样？\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	model := probe.DefaultConfig()
	model.BaseURL = provider.URL
	model.APIKey = "fixture-only"
	model.Model = "fixture"
	cfg := Config{Agent: model, AccessToken: token, DataDir: t.TempDir()}
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
			t.Fatalf("%s %s: status=%d want=%d %s", method, path, code, want, b)
		}
		if target != nil {
			if err := json.Unmarshal(b, target); err != nil {
				t.Fatal(err)
			}
		}
	}
	count := func() int { providerMu.Lock(); defer providerMu.Unlock(); return calls }
	postEpisode := func(title, content string) companion.Episode {
		t.Helper()
		var e companion.Episode
		decode("POST", "/api/episodes", map[string]any{"title": title, "content": content, "allow_followup": true, "followup_at": time.Now().Add(-time.Minute).Format(time.RFC3339)}, &e, 201)
		return e
	}
	cares := func() []companion.CareItem {
		t.Helper()
		var items []companion.CareItem
		decode("GET", "/api/care", nil, &items, 200)
		return items
	}
	find := func(items []companion.CareItem, episode string) companion.CareItem {
		t.Helper()
		for _, item := range items {
			if item.EpisodeID == episode {
				return item
			}
		}
		t.Fatal("care item missing")
		return companion.CareItem{}
	}
	if code, _ := request("GET", "/api/episodes", nil, false); code != 401 {
		t.Fatal("events not authenticated")
	}
	var settings companion.CareSettings
	decode("GET", "/api/care/settings", nil, &settings, 200)
	if settings.Enabled || settings.AutoGenerate || settings.Revision != 0 {
		t.Fatal("care enabled by default")
	}
	e := postEpisode("周五面试", "面试地点甲楼")
	if len(cares()) != 0 || count() != 0 {
		t.Fatal("disabled care performed work")
	}
	var session sessionInfo
	decode("POST", "/api/sessions", map[string]string{}, &session, 201)
	code, events := request("POST", "/api/sessions/"+session.ID+"/messages", map[string]string{"content": "面试地点甲楼，帮我聊聊这件事"}, true)
	if code != 200 || !strings.Contains(string(events), "event: done") {
		t.Fatalf("fixture chat failed: %d %s", code, events)
	}
	decode("PATCH", "/api/episodes/"+e.ID, map[string]any{"revision": e.Revision, "content": "面试改到乙楼"}, &e, 200)
	floor, err := app.life.Floor("companion", session.ID)
	if err != nil || floor < 2 {
		t.Fatal("event correction did not persist history floor")
	}
	code, events = request("POST", "/api/sessions/"+session.ID+"/messages", map[string]string{"content": "面试在哪里"}, true)
	if code != 200 || !strings.Contains(string(events), "event: done") {
		t.Fatal("chat after correction failed")
	}
	providerMu.Lock()
	captured := lastContext
	providerMu.Unlock()
	if strings.Contains(captured, "甲楼") || !strings.Contains(captured, "乙楼") {
		t.Fatal("stale event leaked from prior context")
	}
	decode("PATCH", "/api/care/settings", map[string]any{"revision": 0, "enabled": true, "auto_generate": false, "daily_limit": 1, "quiet_start": "00:00", "quiet_end": "00:00"}, &settings, 200)
	c := find(cares(), e.ID)
	if c.Status != "due" {
		t.Fatal("event followup was not queued")
	}
	before := count()
	var check struct {
		Generated bool `json:"generated"`
	}
	decode("POST", "/api/care/check", nil, &check, 200)
	if check.Generated || count() != before {
		t.Fatal("automatic care ran without consent")
	}
	decode("POST", "/api/care/"+c.ID+"/generate", nil, &c, 200)
	if c.Status != "ready" || c.Content == "" || count() != before+1 {
		t.Fatal("consented care was not delivered")
	}
	if code, _ := request("POST", "/api/care/"+c.ID+"/generate", nil, true); code != 409 || count() != before+1 {
		t.Fatal("completed care generated twice")
	}
	decode("PATCH", "/api/care/"+c.ID, map[string]any{"revision": c.Revision, "status": "seen"}, &c, 200)
	second := postEpisode("再聊一本书", "聊到一半，下次继续")
	secondCare := find(cares(), second.ID)
	if code, _ := request("POST", "/api/care/"+secondCare.ID+"/generate", nil, true); code != 409 || count() != before+1 {
		t.Fatal("daily budget was bypassed")
	}
	decode("PATCH", "/api/episodes/"+second.ID, map[string]any{"revision": second.Revision, "status": "hidden"}, &second, 200)
	if find(cares(), second.ID).Status != "canceled" {
		t.Fatal("hidden event retained pending care")
	}
	local := time.Now().In(app.location)
	decode("PATCH", "/api/care/settings", map[string]any{"revision": settings.Revision, "daily_limit": 2, "quiet_start": local.Add(-time.Minute).Format("15:04"), "quiet_end": local.Add(30 * time.Minute).Format("15:04")}, &settings, 200)
	third := postEpisode("明天的小事", "准备一件小事")
	thirdCare := find(cares(), third.ID)
	if code, _ := request("POST", "/api/care/"+thirdCare.ID+"/generate", nil, true); code != 409 || count() != before+1 {
		t.Fatal("quiet hours were bypassed")
	}
	decode("PATCH", "/api/care/settings", map[string]any{"revision": settings.Revision, "quiet_start": "00:00", "quiet_end": "00:00"}, &settings, 200)
	claimed, err := app.life.ClaimCare("companion", thirdCare.ID, false, time.Now().Add(181*time.Minute), app.location)
	if err != nil || claimed.Status != "generating" {
		t.Fatal("attempt reservation failed")
	}
	srv.Close()
	if err := app.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	start()
	if find(cares(), third.ID).Status != "interrupted" {
		t.Fatal("restart replayed an uncertain generation")
	}
	if code, _ := request("POST", "/api/care/"+thirdCare.ID+"/generate", nil, true); code != 409 || count() != before+1 {
		t.Fatal("interrupted generation retried")
	}
	var other []companion.Episode
	decode("GET", "/api/episodes?role_id=planner", nil, &other, 200)
	if len(other) != 0 {
		t.Fatal("events leaked across roles")
	}
	var otherSettings companion.CareSettings
	decode("GET", "/api/care/settings?role_id=planner", nil, &otherSettings, 200)
	if otherSettings.Enabled || otherSettings.Revision != 0 {
		t.Fatal("care policy leaked across roles")
	}
	decode("DELETE", fmt.Sprintf("/api/episodes/%s?revision=%d", e.ID, e.Revision), nil, nil, 200)
	entries, err := app.life.Context("companion", "甲楼")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.ID == e.ID {
			t.Fatal("deleted event remained in retrieval")
		}
	}
	if n, _ := app.life.Floor("companion", session.ID); n <= floor {
		t.Fatal("deletion failed to advance context floor")
	}
	decode("PATCH", "/api/care/settings", map[string]any{"revision": settings.Revision, "enabled": false}, &settings, 200)
	if settings.Enabled {
		t.Fatal("care did not stop")
	}
}
