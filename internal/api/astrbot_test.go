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

	astrbotwire "chat-agent/internal/channel/astrbot"
	"chat-agent/internal/probe"
)

func TestAstrBotTransportOnlyClosedLoop(t *testing.T) {
	const (
		secret = "astrbot-api-fixture-secret-over-32-characters"
		token  = "astrbot-api-owner-token-over-32-characters"
	)
	var modelCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		modelCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"我在。\\n\\n慢慢说。\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()

	agent := probe.DefaultConfig()
	agent.BaseURL, agent.APIKey, agent.Model = provider.URL, "fixture", "fixture"
	app, err := New(Config{Agent: agent, DataDir: t.TempDir(), AccessToken: token, AstrBot: AstrBotConfig{Enabled: true, Secret: secret, RoleID: "companion", ClaimTimeout: 10 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	t.Cleanup(func() { _ = app.Close(context.Background()) })

	post := func(payload map[string]any) (int, map[string]any, []byte) {
		t.Helper()
		body, _ := json.Marshal(payload)
		req, _ := http.NewRequest(http.MethodPost, server.URL+"/hooks/astrbot/v1/events", strings.NewReader(string(body)))
		req.Header.Set("X-Chat-Agent-Signature", astrbotwire.Signature(secret, body))
		response, requestErr := server.Client().Do(req)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		defer response.Body.Close()
		raw, _ := io.ReadAll(response.Body)
		value := map[string]any{}
		_ = json.Unmarshal(raw, &value)
		return response.StatusCode, value, body
	}
	event := func(id, text string) map[string]any {
		return map[string]any{"version": 1, "platform": "aiocqhttp", "account_id": "bot-1", "conversation_kind": "private", "conversation_id": "aiocqhttp:FriendMessage:42", "sender_id": "42", "sender_name": "owner", "message_id": id, "text": text, "occurred_at": "2026-11-16T00:00:00Z"}
	}

	if code, response, _ := post(event("unbound", "你好")); code != http.StatusForbidden || response["reply"] == "" || modelCalls.Load() != 0 {
		t.Fatalf("unbound event code=%d response=%v calls=%d", code, response, modelCalls.Load())
	}
	code, _, err := app.identities.Issue(ownerPrincipalID, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if status, response, _ := post(event("bind", "绑定 "+code)); status != http.StatusOK || response["accepted"] != true {
		t.Fatalf("bind status=%d response=%v", status, response)
	}
	normal := event("normal", "今天有点累")
	if status, _, _ := post(normal); status != http.StatusAccepted {
		t.Fatalf("normal status=%d", status)
	}

	var delivery map[string]any
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, server.URL+"/hooks/astrbot/v1/deliveries/next", nil)
		req.Header.Set("X-Chat-Agent-Gateway-Token", secret)
		response, requestErr := server.Client().Do(req)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		if response.StatusCode == http.StatusOK {
			if err := json.NewDecoder(response.Body).Decode(&delivery); err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			break
		}
		response.Body.Close()
		time.Sleep(25 * time.Millisecond)
	}
	messages, _ := delivery["messages"].([]any)
	if modelCalls.Load() != 1 || delivery["target"] != "aiocqhttp:FriendMessage:42" || len(messages) != 2 {
		t.Fatalf("delivery=%v calls=%d", delivery, modelCalls.Load())
	}
	deliveryID, _ := delivery["delivery_id"].(string)
	ack, _ := json.Marshal(map[string]string{"delivery_id": deliveryID, "state": "sent"})
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/hooks/astrbot/v1/deliveries/"+deliveryID+"/ack", strings.NewReader(string(ack)))
	req.Header.Set("X-Chat-Agent-Signature", astrbotwire.Signature(secret, ack))
	response, err := server.Client().Do(req)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("ack status=%v err=%v", response.StatusCode, err)
	}
	response.Body.Close()

	if status, _, _ := post(normal); status != http.StatusAccepted {
		t.Fatalf("duplicate status=%d", status)
	}
	time.Sleep(100 * time.Millisecond)
	if modelCalls.Load() != 1 {
		t.Fatal("duplicate AstrBot event called the model again")
	}
	group := event("group", "群消息")
	group["conversation_kind"], group["conversation_id"] = "group", "aiocqhttp:GroupMessage:9"
	if status, _, _ := post(group); status != http.StatusNoContent || modelCalls.Load() != 1 {
		t.Fatal("disabled group reached the model")
	}
}
