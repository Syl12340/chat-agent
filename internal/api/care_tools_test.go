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
	"sync"
	"testing"
	"time"

	"chat-agent/internal/companion"
	"chat-agent/internal/probe"
)

// One integrated fixture covers natural tool selection, atomic pacing, snooze,
// relevant context, stopping an in-flight delivery and durable recovery.
func TestCarePacingLifecycle(t *testing.T) {
	const token = "care-pacing-fixture-token-over-32-characters"
	const snoozeText = "这段时间我想安静一点，晚些再来找我聊吧。"
	const resumeText = "现在可以恢复之前的关怀安排了。"
	now := time.Now().UTC()
	until := now.Add(90 * time.Minute)
	var mu sync.Mutex
	lastGeneration := ""
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []probe.Message   `json:"messages"`
			Tools    []json.RawMessage `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			http.Error(w, "bad fixture", 400)
			return
		}
		last := body.Messages[len(body.Messages)-1]
		user := ""
		for i := len(body.Messages) - 1; i >= 0; i-- {
			if body.Messages[i].Role == "user" {
				user = body.Messages[i].Content
				break
			}
		}
		delta := map[string]any{"content": "按你的节奏来。"}
		finish := "stop"
		if len(body.Tools) == 0 {
			contents := make([]string, 0, len(body.Messages))
			for _, message := range body.Messages {
				contents = append(contents, message.Content)
			}
			mu.Lock()
			lastGeneration = strings.Join(contents, "\n")
			mu.Unlock()
			delta["content"] = "面试准备得怎么样？今天想聊哪一点都可以。"
		} else if user == snoozeText || user == resumeText {
			var args map[string]any
			if last.Role == "user" {
				args = map[string]any{"action": "read"}
			} else if last.Role == "tool" {
				var p companion.CareSettings
				if json.Unmarshal([]byte(last.Content), &p) == nil {
					if user == snoozeText && p.SnoozeUntil == nil {
						args = map[string]any{"action": "snooze", "revision": p.Revision, "evidence": user, "snooze_until": until.Format(time.RFC3339)}
					}
					if user == resumeText && p.SnoozeUntil != nil {
						args = map[string]any{"action": "resume", "revision": p.Revision, "evidence": user}
					}
				}
			}
			if args != nil {
				input, _ := json.Marshal(args)
				delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("care-%d", len(body.Messages)), "type": "function", "function": map[string]string{"name": "care_preferences", "arguments": string(input)}}}}
				finish = "tool_calls"
			}
		}
		b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", b)
	}))
	defer provider.Close()
	c := probe.DefaultConfig()
	c.BaseURL, c.Model, c.APIKey = provider.URL, "care-pacing", "fixture-only"
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
	request := func(method, path string, body, target any, want int) {
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
	var p companion.CareSettings
	request("GET", "/api/care/settings", nil, &p, 200)
	if p.Enabled || p.MinIntervalMinutes != 180 || p.SnoozeUntil != nil {
		t.Fatal("new care is not opt-in with default pacing")
	}
	request("PATCH", "/api/care/settings", map[string]any{"revision": p.Revision, "enabled": true, "daily_limit": 5, "quiet_start": "00:00", "quiet_end": "00:00", "min_interval_minutes": 15}, &p, 200)
	if err := app.runtime.Create("care-context-session", "companion"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.memory.Create("companion", "面试时喜欢提前十分钟到场", "approved", "user", ""); err != nil {
		t.Fatal(err)
	}
	_, _ = app.memory.Create("companion", "面试未审核秘密", "pending", "user", "")
	_, _ = app.memory.Create("planner", "面试跨角色秘密", "approved", "user", "")
	due := now.Add(-time.Minute)
	var episodes []companion.Episode
	for i := 0; i < 3; i++ {
		e, err := app.life.AddEpisode("companion", companion.EpisodeDraft{Title: fmt.Sprintf("面试准备%d", i), Content: "面试改到乙楼，想聊准备情况。", Status: "open", AllowFollowup: true, FollowupAt: &due, SourceText: "甲楼旧引用"})
		if err != nil {
			t.Fatal(err)
		}
		episodes = append(episodes, e)
	}
	var items []companion.CareItem
	request("GET", "/api/care", nil, &items, 200)
	find := func(id string) companion.CareItem {
		for _, item := range items {
			if item.EpisodeID == id {
				return item
			}
		}
		t.Fatal("care missing")
		return companion.CareItem{}
	}
	first, second, third := find(episodes[0].ID), find(episodes[1].ID), find(episodes[2].ID)
	request("POST", "/api/care/"+first.ID+"/generate", nil, &first, 200)
	mu.Lock()
	captured := lastGeneration
	mu.Unlock()
	if first.Status != "ready" || first.AttemptedAt == nil || !strings.Contains(captured, "提前十分钟") || strings.Contains(captured, "未审核秘密") || strings.Contains(captured, "跨角色秘密") || strings.Contains(captured, "甲楼旧引用") {
		t.Fatal("care context scope or persisted attempt failed")
	}
	request("POST", "/api/care/"+second.ID+"/generate", nil, nil, 409)
	claimed, err := app.life.ClaimCare("companion", second.ID, false, now.Add(16*time.Minute), app.location)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = app.life.FinishCare("companion", second.ID, claimed.Revision, "第二条已交付的问候", ""); err != nil {
		t.Fatal(err)
	}
	var session sessionInfo
	request("POST", "/api/sessions", map[string]string{}, &session, 201)
	chat := func(text string) {
		t.Helper()
		b, _ := json.Marshal(map[string]string{"content": text})
		r, _ := http.NewRequest("POST", srv.URL+"/api/sessions/"+session.ID+"/messages", strings.NewReader(string(b)))
		r.Header.Set("Authorization", "Bearer "+token)
		res, err := srv.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		output, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 || !strings.Contains(string(output), "event: done") {
			t.Fatalf("care tool chat: %d %s", res.StatusCode, output)
		}
	}
	chat(snoozeText)
	request("GET", "/api/care/settings", nil, &p, 200)
	if p.SnoozeUntil == nil || !p.Enabled {
		t.Fatal("natural snooze changed opt-in or failed")
	}
	if _, err = app.life.ClaimCare("companion", third.ID, false, now.Add(32*time.Minute), app.location); err == nil {
		t.Fatal("snooze bypassed despite sufficient interval")
	}
	request("PATCH", "/api/care/settings", map[string]any{"revision": p.Revision - 1, "snooze_until": nil}, nil, 409)
	chat(resumeText)
	request("GET", "/api/care/settings", nil, &p, 200)
	if p.SnoozeUntil != nil || !p.Enabled || p.MinIntervalMinutes != 15 {
		t.Fatal("resume did not preserve existing plan")
	}
	claimed, err = app.life.ClaimCare("companion", third.ID, false, now.Add(32*time.Minute), app.location)
	if err != nil {
		t.Fatal(err)
	}
	request("PATCH", "/api/care/settings", map[string]any{"revision": p.Revision, "enabled": false}, &p, 200)
	if _, err = app.life.FinishCare("companion", third.ID, claimed.Revision, "不应投递", ""); err == nil {
		t.Fatal("disable allowed in-flight delivery")
	}
	if err = app.life.AdvanceCuts("companion", map[string]int{session.ID: 3}); err != nil {
		t.Fatal(err)
	}
	if err = app.life.AdvanceCuts("companion", map[string]int{session.ID: 1}); err != nil {
		t.Fatal(err)
	}
	if floor, _ := app.life.Floor("companion", session.ID); floor != 3 {
		t.Fatal("memory barrier could move backwards")
	}
	srv.Close()
	if err = app.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Simulate an old v1 setting lacking the added interval field.
	path := filepath.Join(cfg.DataDir, "life", "state.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]json.RawMessage
	_ = json.Unmarshal(raw, &state)
	var settings map[string]map[string]json.RawMessage
	_ = json.Unmarshal(state["settings"], &settings)
	delete(settings["companion"], "min_interval_minutes")
	state["settings"], _ = json.Marshal(settings)
	raw, _ = json.Marshal(state)
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	start()
	request("GET", "/api/care/settings", nil, &p, 200)
	if p.MinIntervalMinutes != 180 || p.Enabled {
		t.Fatal("legacy snapshot normalization or disabled state lost")
	}
	if _, err = app.life.ClaimCare("companion", third.ID, false, now.Add(48*time.Hour), app.location); err == nil {
		t.Fatal("disabled plan performed work after restart")
	}
	var other companion.CareSettings
	request("GET", "/api/care/settings?role_id=planner", nil, &other, 200)
	if other.Enabled || other.MinIntervalMinutes != 180 {
		t.Fatal("care settings leaked across roles")
	}
}
