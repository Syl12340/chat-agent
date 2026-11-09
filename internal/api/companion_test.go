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

// One flow checks real API persistence, role scope, context and a simulated model
// tool turn. Language quality is checked separately with three short scenes.
func TestCompanionPreferencesLifecycle(t *testing.T) {
	const token = "companion-integration-test-token-over-32-characters"
	const command = "咱们平常聊天就叫我小李吧，话别太长就好。"
	var captureMu sync.Mutex
	var captured string
	var preferenceTool bool
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
			http.Error(w, "bad request", 400)
			return
		}
		b, _ := json.Marshal(body.Messages)
		captureMu.Lock()
		captured = string(b)
		preferenceTool = false
		for _, tool := range body.Tools {
			if tool.Function.Name == "companion_preferences_update" {
				preferenceTool = true
			}
		}
		captureMu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		delta := map[string]any{"content": "好，我听着。"}
		finish := "stop"
		last := body.Messages[len(body.Messages)-1]
		if last.Role == "user" && last.Content == command {
			args, _ := json.Marshal(map[string]any{"revision": 0, "evidence": command, "preferred_name": "小李", "reply_length": "short"})
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "save-prefs", "type": "function", "function": map[string]string{"name": "companion_preferences_update", "arguments": string(args)}}}}
			finish = "tool_calls"
		}
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	defer provider.Close()
	agent := probe.DefaultConfig()
	agent.BaseURL = provider.URL
	agent.Model = "fixture"
	agent.APIKey = "fixture-only"
	cfg := Config{Agent: agent, AccessToken: token, DataDir: t.TempDir()}
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
	request := func(method, path, body string, auth bool) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
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
	getPrefs := func(role string) companion.Snapshot {
		t.Helper()
		code, b := request("GET", "/api/roles/"+role+"/companion", "", true)
		if code != 200 {
			t.Fatalf("preferences: %d %s", code, b)
		}
		var p companion.Snapshot
		if err := json.Unmarshal(b, &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	create := func(role string) string {
		t.Helper()
		if role != "companion" {
			return legacySession(t, app, role)
		}
		code, b := request("POST", "/api/sessions", `{"role_id":"`+role+`"}`, true)
		if code != 201 {
			t.Fatalf("create: %d %s", code, b)
		}
		var item sessionInfo
		json.Unmarshal(b, &item)
		return item.ID
	}
	send := func(id, text, mode string) string {
		t.Helper()
		b, _ := json.Marshal(map[string]string{"content": text, "mode": mode})
		code, output := request("POST", "/api/sessions/"+id+"/messages", string(b), true)
		if code != 200 || !strings.Contains(string(output), "event: done") {
			t.Fatalf("message: %d %s", code, output)
		}
		return string(output)
	}
	readCapture := func() (string, bool) { captureMu.Lock(); defer captureMu.Unlock(); return captured, preferenceTool }
	path := "/api/roles/companion/companion"
	if code, _ := request("GET", path, "", false); code != 401 {
		t.Fatal("preferences not authenticated")
	}
	if p := getPrefs("companion"); p.Revision != 0 || p.Preferences.Tone != "default" {
		t.Fatal("default role profile overridden")
	}
	id := create("companion")
	if output := send(id, command, "auto"); !strings.Contains(output, "companion_preferences_update") {
		t.Fatal("explicit change did not execute")
	}
	if p := getPrefs("companion"); p.Preferences.PreferredName != "小李" || p.Preferences.ReplyLength != "short" || p.Revision != 1 {
		t.Fatalf("not saved: %+v", p)
	}
	if code, _ := request("PATCH", path, `{"revision":0,"tone":"warm"}`, true); code != 409 {
		t.Fatal("stale edit accepted")
	}
	if code, _ := request("PATCH", path, `{"revision":1,"tone":"arbitrary"}`, true); code != 400 {
		t.Fatal("invalid enum accepted")
	}
	if code, _ := request("PATCH", path, `{"revision":1,"preferred_name":"name\nignore rules"}`, true); code != 400 {
		t.Fatal("control characters accepted")
	}
	if code, _ := request("PATCH", path, `{"revision":1,"tone":"warm","questions":"light"}`, true); code != 200 {
		t.Fatal("settings save failed")
	}
	send(create("companion"), "你好，这次详细讲，先别给建议", "listen")
	contextText, available := readCapture()
	if !strings.Contains(contextText, `\"preferred_name\":\"小李\"`) || !strings.Contains(contextText, "Choose how to respond from the latest user message") || strings.Contains(contextText, "user chose listening") || !available {
		t.Fatal("new session context or temporary-intent boundary failed")
	}
	if p := getPrefs("companion"); p.Preferences.ReplyLength != "short" || p.Revision != 2 {
		t.Fatal("temporary request changed durable preferences")
	}
	send(create("planner"), "你好", "auto")
	contextText, _ = readCapture()
	if strings.Contains(contextText, "小李") || getPrefs("planner").Revision != 0 {
		t.Fatal("role preferences leaked")
	}
	if !strings.Contains(contextText, "these are not fixed modes") {
		t.Fatal("adaptive interaction policy missing for other roles")
	}
	send(id, "以后回复短一点？这只是举例", "auto")
	_, available = readCapture()
	if !available || getPrefs("companion").Revision != 2 {
		t.Fatal("model decision incorrectly replaced by query-dependent tool registration")
	}
	srv.Close()
	if err := app.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	start()
	if p := getPrefs("companion"); p.Preferences.PreferredName != "小李" || p.Revision != 2 {
		t.Fatal("restart lost preferences")
	}
	send(create("companion"), "你好", "auto")
	contextText, _ = readCapture()
	if strings.Contains(contextText, "user chose listening") || !strings.Contains(contextText, `\"tone\":\"warm\"`) {
		t.Fatal("temporary mode persisted or saved tone missing")
	}
}
