package api

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pocket-agent/internal/media"
	"pocket-agent/internal/probe"
)

type imageChatFixture struct {
	t     *testing.T
	calls atomic.Int32
}

func (m *imageChatFixture) Stream(_ context.Context, request probe.ModelRequest, emit func(probe.Event)) (probe.Message, error) {
	call := m.calls.Add(1)
	if request.Model != "ecnu-plus" || request.Thinking != "enabled" || request.ReasoningEffort != "xhigh" || request.MaxOutputTokens != 0 {
		m.t.Error("model/thinking selection did not reach prepared request")
	}
	visibleImage, echo := false, false
	for _, message := range request.Messages {
		for _, picture := range message.Images {
			visibleImage = visibleImage || strings.HasPrefix(picture.DataURL, "data:image/png;base64,")
		}
		echo = echo || message.ReasoningContent == "private-protocol-fixture"
	}
	if !visibleImage {
		m.t.Error("saved image did not reach model projection")
	}
	for _, tool := range request.Tools {
		if tool.Name == "memory_write" || tool.Name == "role_change_propose" {
			m.t.Error("image-only input acquired write authority")
		}
	}
	if call == 1 {
		emit(probe.Event{Type: probe.EventThinking})
		emit(probe.Event{Type: probe.EventReasoningDelta, Text: "private-protocol-fixture"})
		return probe.Message{Role: "assistant", ReasoningContent: "private-protocol-fixture", ToolCalls: []probe.ToolCall{{ID: "fixture-clock", Type: "function", Function: probe.FunctionCall{Name: "get_current_time", Arguments: `{}`}}}}, nil
	}
	if !echo {
		m.t.Error("tool reasoning protocol was not echoed")
	}
	emit(probe.Event{Type: probe.EventThinking})
	emit(probe.Event{Type: probe.EventReasoningDelta, Text: "final-thinking-fixture"})
	emit(probe.Event{Type: probe.EventTextDelta, Text: "这是本地模拟图片回答。"})
	return probe.Message{Role: "assistant", Content: "这是本地模拟图片回答。", ReasoningContent: "final-thinking-fixture"}, nil
}

func TestImageChatLifecycle(t *testing.T) {
	model := &imageChatFixture{t: t}
	c := probe.DefaultConfig()
	c.BaseURL = "https://chat.ecnu.edu.cn/open/api/v1"
	c.Model = "ecnu-max"
	c.Provider = model
	const token = "image-chat-fixture-token-at-least-32"
	s, err := New(Config{Agent: c, DataDir: t.TempDir(), AccessToken: token})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	request := func(method, path string, body io.Reader, auth bool, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, body)
		if auth {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		out := httptest.NewRecorder()
		s.Handler().ServeHTTP(out, req)
		if out.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, out.Code, out.Body.String())
		}
		return out
	}
	jsonRequest := func(method, path string, body any, want int) *httptest.ResponseRecorder {
		t.Helper()
		data, _ := json.Marshal(body)
		return request(method, path, bytes.NewReader(data), true, want)
	}
	request("GET", "/api/chat/options", nil, false, 401)
	options := request("GET", "/api/chat/options", nil, true, 200)
	if !strings.Contains(options.Body.String(), "ecnu-plus") || strings.Contains(options.Body.String(), "APIKey") {
		t.Fatal("invalid public options")
	}
	newSession := func() string {
		out := jsonRequest("POST", "/api/sessions", map[string]string{"role_id": "companion"}, 201)
		var session sessionInfo
		if json.Unmarshal(out.Body.Bytes(), &session) != nil {
			t.Fatal("bad session response")
		}
		return session.ID
	}
	id, other := newSession(), newSession()
	picture := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	picture.Set(0, 0, color.NRGBA{R: 255, A: 255})
	var encoded bytes.Buffer
	if err = png.Encode(&encoded, picture); err != nil {
		t.Fatal(err)
	}
	upload := func() media.Image {
		out := request("POST", "/api/sessions/"+id+"/images", bytes.NewReader(encoded.Bytes()), true, 201)
		var item media.Image
		if err = json.Unmarshal(out.Body.Bytes(), &item); err != nil {
			t.Fatal(err)
		}
		return item
	}
	first := upload()
	request("GET", "/api/sessions/"+id+"/images/"+first.ID, nil, false, 401)
	request("GET", "/api/sessions/"+other+"/images/"+first.ID, nil, true, 404)
	base := "/api/sessions/" + id
	body := map[string]any{"request_id": "image-request", "content": "", "model": "ecnu-plus", "thinking": "ultra", "image_ids": []string{first.ID}}
	invalid := map[string]any{"request_id": "bad-selection", "content": "fixture", "model": "ecnu-embedding-small", "thinking": "off"}
	jsonRequest("POST", base+"/turns", invalid, 400)
	jsonRequest("POST", base+"/turns", body, 202)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		turn, readErr := s.turns.Get(id, "image-request")
		if readErr == nil && turn.Status == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	turn, err := s.turns.Get(id, "image-request")
	if err != nil || turn.Status != "completed" || model.calls.Load() != 2 {
		t.Fatalf("image turn did not complete once: %+v %v", turn, err)
	}
	jsonRequest("POST", base+"/turns", body, 200)
	body["thinking"] = "high"
	jsonRequest("POST", base+"/turns", body, 409)
	request("DELETE", base+"/images/"+first.ID, nil, true, 409)
	history := request("GET", base+"/messages", nil, true, 200)
	request("GET", base+"/messages", nil, false, 401)
	if !strings.Contains(history.Body.String(), "private-protocol-fixture") || !strings.Contains(history.Body.String(), "final-thinking-fixture") || strings.Contains(history.Body.String(), "data:image") || !strings.Contains(history.Body.String(), first.ID) || turn.ReasoningContent != "private-protocol-fixture\n\nfinal-thinking-fixture" {
		t.Fatal("authorized history lost reasoning or image reference, or exposed inline image data")
	}
	session, err := s.session(id)
	if err != nil {
		t.Fatal(err)
	}
	protocolRetained := false
	for _, message := range session.Messages() {
		protocolRetained = protocolRetained || message.ReasoningContent == "private-protocol-fixture"
	}
	if !protocolRetained {
		t.Fatal("history rendering destroyed private provider state")
	}
	draft := upload()
	request("DELETE", base+"/turns/image-request?purge=1", nil, true, 200)
	request("GET", base+"/images/"+first.ID, nil, true, 404)
	request("GET", base+"/images/"+draft.ID, nil, true, 200)
	request("DELETE", base, nil, true, 200)
	if _, _, err = s.images.Get("companion", id, draft.ID); err == nil {
		t.Fatal("session delete left media behind")
	}
	if model.calls.Load() != 2 {
		t.Fatal("read/delete/retry caused extra model generation")
	}
}
