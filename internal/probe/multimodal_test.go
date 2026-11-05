package probe

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pocket-agent/internal/media"
)

type multimodalTransport func(*http.Request) (*http.Response, error)

func (f multimodalTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMultimodalThinkingLifecycle(t *testing.T) {
	dir := t.TempDir()
	sess, err := OpenSession(dir, AgentID, "images")
	if err != nil {
		t.Fatal(err)
	}
	if err = sess.BindRole("companion"); err != nil {
		t.Fatal(err)
	}
	image := media.Image{ID: "0123456789abcdef0123456789abcdef", MIMEType: "image/png", Size: 4, Width: 1, Height: 1}
	const url = "data:image/png;base64,dGVzdA=="
	requests, thinkingEvents := 0, 0
	provider := &ChatCompletionsProvider{endpoint: "https://fixture.example/chat/completions", key: "fixture-only", client: &http.Client{Transport: multimodalTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if string(body["model"]) != `"ecnu-max"` || string(body["reasoning_effort"]) != `"high"` || !strings.Contains(string(body["thinking"]), "enabled") {
			t.Fatal("model selection not sent")
		}
		var messages []map[string]json.RawMessage
		if err := json.Unmarshal(body["messages"], &messages); err != nil {
			t.Fatal(err)
		}
		foundImage, foundReasoning := false, false
		imageCount := 0
		for _, message := range messages {
			if strings.Contains(string(message["content"]), url) {
				foundImage = true
				if message["images"] != nil {
					t.Fatal("host metadata leaked into provider")
				}
				var parts []map[string]json.RawMessage
				if json.Unmarshal(message["content"], &parts) != nil {
					t.Fatal("invalid multimodal content")
				}
				for _, part := range parts {
					if string(part["type"]) == `"image_url"` {
						imageCount++
					}
				}
			}
			if strings.Contains(string(message["reasoning_content"]), "private-reasoning-fixture") {
				foundReasoning = true
			}
		}
		if !foundImage || imageCount != 3 || requests > 1 && !foundReasoning {
			t.Fatal("image or tool reasoning not carried forward")
		}
		stream := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"visible reply\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
		if requests == 1 {
			stream = "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"private-reasoning-fixture\"}}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"clock1\",\"type\":\"function\",\"function\":{\"name\":\"get_current_time\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}, nil
	})}}
	c := DefaultConfig()
	c.Model = "ecnu-max"
	c.Provider = provider
	c.Thinking = "enabled"
	c.ReasoningEffort = "high"
	c.ResolveImage = func(context.Context, media.Image) (string, error) { return url, nil }
	a, err := New(c, sess)
	if err != nil {
		t.Fatal(err)
	}
	emit := func(event Event) {
		if event.Type == EventThinking {
			thinkingEvents++
			if event.Text != "" {
				t.Fatal("hidden reasoning was exposed")
			}
		}
	}
	second, third, fourth := image, image, image
	second.ID = "2223456789abcdef0123456789abcdef"
	third.ID = "3323456789abcdef0123456789abcdef"
	fourth.ID = "4423456789abcdef0123456789abcdef"
	if err = RunMessages(context.Background(), a, time.Second, []Message{{Role: "user", Images: []media.Image{image, second, third}}}, emit); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || thinkingEvents != 1 {
		t.Fatal("unexpected model calls")
	}
	if err = RunMessages(context.Background(), a, time.Second, []Message{{Role: "user", Content: "continue", Images: []media.Image{fourth}}}, emit); err != nil {
		t.Fatal(err)
	}
	messages := sess.Messages()
	messages[0].Images[0].ID = "mutated"
	if sess.Messages()[0].Images[0].ID != image.ID || sess.Messages()[0].Images[0].DataURL != "" {
		t.Fatal("session image aliased or contains base64")
	}
	if err = sess.Close(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, AgentID, "images.jsonl"))
	if !strings.Contains(string(raw), `"version":2`) || strings.Contains(string(raw), url) {
		t.Fatal("transcript format or image persistence wrong")
	}
	sess, err = OpenSession(dir, AgentID, "images")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if _, err = sess.DropTail(2); err != nil {
		t.Fatal(err)
	}
	if len(sess.Messages()) != 4 || sess.Messages()[1].ReasoningContent != "private-reasoning-fixture" {
		t.Fatal("tool reasoning failed roundtrip or purge")
	}
	if _, err = sess.DropTail(len(sess.Messages())); err != nil {
		t.Fatal(err)
	}
	if len(sess.Messages()) != 0 {
		t.Fatal("image turn was not removed")
	}
}
