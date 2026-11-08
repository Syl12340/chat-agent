package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pocket-agent/internal/channel/onebot"
	"pocket-agent/internal/probe"
)

func TestOneBotOwnerPrivateClosedLoop(t *testing.T) {
	const (
		account = "10001"
		owner   = "20002"
		ingress = "onebot-ingress-fixture-secret-over-32-characters"
		action  = "onebot-action-fixture-token-over-32-characters"
		token   = "onebot-server-fixture-token-over-32-characters"
	)
	var modelCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"QQ 回复\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	var actionCalls atomic.Int32
	actionReceived := make(chan struct{}, 2)
	actionServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/send_private_msg" || r.Header.Get("Authorization") != "Bearer "+action {
			t.Errorf("unexpected action request: %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var payload struct {
			UserID  int64 `json:"user_id"`
			Message []struct {
				Type string            `json:"type"`
				Data map[string]string `json:"data"`
			} `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.UserID != 20002 || len(payload.Message) != 1 || payload.Message[0].Data["text"] != "QQ 回复" {
			t.Errorf("unexpected action payload: %#v err=%v", payload, err)
		}
		actionCalls.Add(1)
		actionReceived <- struct{}{}
		writeJSON(w, 200, map[string]any{"status": "ok", "retcode": 0, "data": map[string]int{"message_id": 9}})
	}))
	defer actionServer.Close()

	agent := probe.DefaultConfig()
	agent.BaseURL = provider.URL
	agent.APIKey = "fixture"
	agent.Model = "channel-fixture"
	dataDir := t.TempDir()
	config := Config{Agent: agent, DataDir: dataDir, AccessToken: token, OneBot: OneBotConfig{Enabled: true, AccountID: account, OwnerID: owner, IngressSecret: ingress, ActionBaseURL: actionServer.URL, ActionToken: action, RoleID: "companion", Timeout: 2 * time.Second}}
	start := func() (*Server, *httptest.Server) {
		app, err := New(config)
		if err != nil {
			t.Fatal(err)
		}
		return app, httptest.NewServer(app.Handler())
	}
	app, server := start()
	closed := false
	t.Cleanup(func() {
		if !closed {
			server.Close()
			_ = app.Close(context.Background())
		}
	})
	post := func(body []byte, signature string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, server.URL+"/hooks/onebot/v1/events", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Self-ID", account)
		req.Header.Set("X-Signature", signature)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	body := []byte(`{"time":1794067200,"self_id":10001,"post_type":"message","message_type":"private","sub_type":"friend","message_id":7,"user_id":20002,"message":[{"type":"text","data":{"text":"你好"}}]}`)
	if code := post(body, onebot.Signature(ingress, body)); code != http.StatusNoContent {
		t.Fatalf("owner webhook status=%d", code)
	}
	select {
	case <-actionReceived:
	case <-time.After(5 * time.Second):
		t.Fatal("OneBot action was not delivered")
	}
	if modelCalls.Load() != 1 || actionCalls.Load() != 1 {
		t.Fatalf("calls model=%d action=%d", modelCalls.Load(), actionCalls.Load())
	}
	if code := post(body, onebot.Signature(ingress, body)); code != http.StatusNoContent {
		t.Fatalf("duplicate status=%d", code)
	}
	time.Sleep(150 * time.Millisecond)
	if modelCalls.Load() != 1 || actionCalls.Load() != 1 {
		t.Fatal("duplicate event repeated model or delivery")
	}
	if code := post(body, onebot.Signature("wrong", body)); code != http.StatusUnauthorized {
		t.Fatalf("bad signature status=%d", code)
	}
	guest := []byte(`{"self_id":10001,"post_type":"message","message_type":"private","sub_type":"friend","message_id":8,"user_id":30003,"message":"guest"}`)
	if code := post(guest, onebot.Signature(ingress, guest)); code != http.StatusNoContent {
		t.Fatalf("guest status=%d", code)
	}
	if modelCalls.Load() != 1 {
		t.Fatal("guest reached the model")
	}
	server.Close()
	if err := app.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	closed = true
	reopened, reopenedServer := start()
	time.Sleep(150 * time.Millisecond)
	if actionCalls.Load() != 1 {
		t.Fatal("sent outbox replayed after restart")
	}
	reopenedServer.Close()
	if err := reopened.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
