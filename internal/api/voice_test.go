package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pocket-agent/internal/probe"
)

// One offline flow covers authorization, bounded input, explicit synthesis,
// cost guards, error redaction and cancellation without a paid model call.
func TestSpeechLifecycle(t *testing.T) {
	const token = "speech-test-access-token-at-least-32-characters"
	var calls atomic.Int32
	started := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/audio/speech" || r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Error("wrong provider endpoint or credentials")
		}
		var body struct{ Input, Model, Voice, ResponseFormat string }
		var raw map[string]any
		_ = json.NewDecoder(r.Body).Decode(&raw)
		body.Input, _ = raw["input"].(string)
		body.Model, _ = raw["model"].(string)
		body.Voice, _ = raw["voice"].(string)
		body.ResponseFormat, _ = raw["response_format"].(string)
		if body.Model != "fixture-tts" || body.Voice != "female_sweet" || body.ResponseFormat != "mp3" {
			t.Error("wrong speech configuration")
		}
		if body.Input == "failure" {
			http.Error(w, "upstream-private-detail fixture-key", 500)
			return
		}
		if body.Input == "cancel" {
			close(started)
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("ID3-fixture-only"))
	}))
	defer provider.Close()
	c := probe.DefaultConfig()
	c.BaseURL, c.APIKey, c.Model = provider.URL, "fixture-key", "fixture-chat"
	app, err := New(Config{Agent: c, AccessToken: token, DataDir: t.TempDir(), Speech: SpeechConfig{Model: "fixture-tts"}})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(context.Background())
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	request := func(body, auth string) (int, string, http.Header) {
		t.Helper()
		r, _ := http.NewRequest("POST", server.URL+"/api/voice/speech", strings.NewReader(body))
		if auth != "" {
			r.Header.Set("Authorization", "Bearer "+auth)
		}
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		b, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(b), response.Header
	}
	if code, _, _ := request(`{"input":"你好"}`, ""); code != 401 {
		t.Fatal("speech must require authentication")
	}
	if code, _, _ := request(`{"input":"`+strings.Repeat("字", 1201)+`"}`, token); code != 400 {
		t.Fatal("input must be bounded")
	}
	if code, _, _ := request(`{"input":"你好"} {}`, token); code != 400 {
		t.Fatal("trailing JSON must be rejected")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid requests invoked provider")
	}
	code, body, headers := request(`{"input":"你好"}`, token)
	if code != 200 || body != "ID3-fixture-only" || headers.Get("Content-Type") != "audio/mpeg" || headers.Get("Cache-Control") != "no-store" {
		t.Fatal("speech response must be private audio")
	}
	if code, _, _ := request(`{"input":"你好"}`, token); code != 429 || calls.Load() != 1 {
		t.Fatal("repeated synthesis must be limited")
	}
	reset := func() { app.mu.Lock(); app.speechAttempt = time.Time{}; app.mu.Unlock() }
	reset()
	if code, body, _ := request(`{"input":"failure"}`, token); code != 502 || strings.Contains(body, "fixture-key") || strings.Contains(body, "upstream-private-detail") || calls.Load() != 2 {
		t.Fatal("provider failure must be redacted and not retried")
	}
	reset()
	ctx, cancel := context.WithCancel(context.Background())
	r, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/api/voice/speech", strings.NewReader(`{"input":"cancel"}`))
	r.Header.Set("Authorization", "Bearer "+token)
	done := make(chan struct{})
	go func() {
		response, _ := server.Client().Do(r)
		if response != nil {
			response.Body.Close()
		}
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("speech did not start")
	}
	if code, _, _ := request(`{"input":"parallel"}`, token); code != 429 || calls.Load() != 3 {
		t.Fatal("concurrent synthesis must be rejected")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("speech cancellation did not finish")
	}
}
