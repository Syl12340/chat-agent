package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pocket-agent/internal/probe"
)

// One integration test covers the HTTP lifecycle; runtime protocol tests remain
// in probe, so we do not duplicate their provider fixtures or test matrix here.
func TestHTTPChatLifecycle(t *testing.T) {
	const token = "test-access-token-at-least-32-characters"
	agent := probe.DefaultConfig()
	agent.Demo = true
	agent.APIKey = "private-model-key-not-for-the-browser"
	cfg := Config{Agent: agent, DataDir: t.TempDir(), AccessToken: token}
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
	do := func(method, path, body string, auth bool) *http.Response {
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if auth {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	status := func(resp *http.Response, want int) {
		t.Helper()
		defer resp.Body.Close()
		if resp.StatusCode != want {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("status=%d want=%d: %s", resp.StatusCode, want, b)
		}
	}
	decode := func(resp *http.Response, want int, target any) {
		t.Helper()
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("status=%d want=%d", resp.StatusCode, want)
		}
		if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
			t.Fatal(err)
		}
	}

	status(do("GET", "/api/sessions", "", false), 401)
	status(do("POST", "/api/sessions/missing/cancel", "", false), 401)
	status(do("GET", "/healthz", "", false), 200)
	status(do("POST", "/hooks/onebot/v1/events", `{}`, false), 404)
	page := do("GET", "/", "", false)
	html, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if page.StatusCode != 200 || !strings.Contains(string(html), "chat-agent") || strings.Contains(string(html), "conversation-mode") || strings.Contains(string(html), token) || strings.Contains(string(html), agent.APIKey) {
		t.Fatal("shell unavailable or leaked a secret")
	}
	statusResponse := do("GET", "/api/status", "", true)
	configBody, _ := io.ReadAll(statusResponse.Body)
	statusResponse.Body.Close()
	if statusResponse.StatusCode != 200 || strings.Contains(string(configBody), token) || strings.Contains(string(configBody), agent.APIKey) {
		t.Fatal("status leaked secrets")
	}

	var created sessionInfo
	decode(do("POST", "/api/sessions", "", true), 201, &created)
	base := "/api/sessions/" + created.ID
	var sessions []sessionInfo
	decode(do("GET", "/api/sessions", "", true), 200, &sessions)
	if len(sessions) != 1 || sessions[0].ID != created.ID {
		t.Fatal("created session not listed")
	}
	status(do("POST", base+"/messages", `{"content":"`+strings.Repeat("a", 65537)+`"}`, true), 400)
	stream := do("POST", base+"/messages", `{"content":"现在几点？"}`, true)
	if stream.StatusCode != 200 || !strings.HasPrefix(stream.Header.Get("Content-Type"), "text/event-stream") || stream.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("SSE headers incorrect")
	}
	transcript, err := io.ReadAll(stream.Body)
	stream.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"tool_start", "tool_result", "text_delta", "done"} {
		if !strings.Contains(string(transcript), "event: "+event+"\n") {
			t.Fatalf("missing SSE event %s", event)
		}
	}
	type history struct {
		Messages []probe.Message `json:"messages"`
		Active   bool            `json:"active"`
	}
	var saved history
	decode(do("GET", base+"/messages", "", true), 200, &saved)
	if saved.Active || len(saved.Messages) != 4 || saved.Messages[2].Role != "tool" || saved.Messages[3].Role != "assistant" {
		t.Fatal("completed tool conversation not saved")
	}

	// Cancel through a second request while the first SSE response is open.
	slow := do("POST", base+"/messages", `{"content":"体验流式回复并测试停止"}`, true)
	scanner := bufio.NewScanner(slow.Body)
	started := false
	for scanner.Scan() {
		if scanner.Text() == "event: text_delta" {
			started = true
			break
		}
	}
	if !started {
		slow.Body.Close()
		t.Fatal("stream did not start")
	}
	status(do("POST", base+"/messages", `{"content":"并发提交"}`, true), 409)
	status(do("POST", base+"/reset", "", true), 409)
	status(do("POST", base+"/cancel", "", true), 200)
	var tail strings.Builder
	for scanner.Scan() {
		tail.WriteString(scanner.Text())
		tail.WriteByte('\n')
	}
	slow.Body.Close()
	if scanner.Err() != nil || !strings.Contains(tail.String(), "event: canceled") || strings.Contains(tail.String(), "event: done") {
		t.Fatalf("cancel did not finish SSE correctly: %v", scanner.Err())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := app.Close(ctx); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	app, srv = start()
	decode(do("GET", base+"/messages", "", true), 200, &saved)
	if saved.Active || len(saved.Messages) != 5 || saved.Messages[4].Role != "user" {
		t.Fatal("history not restored, or canceled assistant was persisted")
	}
	status(do("POST", base+"/reset", "", true), 200)
	if err := app.Close(ctx); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	_, srv = start()
	decode(do("GET", base+"/messages", "", true), 200, &saved)
	if len(saved.Messages) != 0 {
		t.Fatal("context reset did not survive restarting")
	}
}
