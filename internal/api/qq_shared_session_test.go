package api

import (
	"context"
	"image"
	"image/color"
	"image/gif"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"chat-agent/internal/channel"
	"chat-agent/internal/probe"
)

func TestQQSharesAppTranscriptAndLoadsAttachments(t *testing.T) {
	cfg := probe.DefaultConfig()
	cfg.Demo = true
	s, err := New(Config{Agent: cfg, DataDir: t.TempDir(), AccessToken: "qq-shared-test-access-token-past-32"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	const appID = "app_existing"
	if err := s.runtime.Create(appID, "companion"); err != nil {
		t.Fatal(err)
	}
	sess, err := s.session(appID)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := s.agent(sess, "App transcript marker", appID)
	if err != nil {
		t.Fatal(err)
	}
	if err := probe.RunMessages(context.Background(), agent, 5*time.Second, []probe.Message{{Role: "user", Content: "App transcript marker"}}, func(probe.Event) {}); err != nil {
		t.Fatal(err)
	}
	_, err = s.memory.Create("companion", "approved memory marker", "approved", "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.memory.Create("companion", "pending memory marker", "pending", "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	store, err := channel.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cdn := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sticker" {
			img := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.White, color.Black})
			if err := gif.Encode(w, img, nil); err != nil {
				t.Error(err)
			}
			return
		}
		_, _ = w.Write([]byte("QQ attachment marker"))
	}))
	t.Cleanup(cdn.Close)
	q := &qqOfficialIntegration{server: s, store: store, config: QQOfficialConfig{RoleID: "companion"}, http: cdn.Client()}
	s.qqofficial = q
	t.Cleanup(func() { s.qqofficial = nil })
	envelope := channel.Envelope{Platform: "qq-official", AccountID: "bot", Conversation: channel.Conversation{Kind: "private", ID: "owner"}}
	mapping, shared, err := q.ensureSession(envelope)
	if err != nil || mapping.SessionID != appID || shared != sess || shared.Messages()[0].Content != "App transcript marker" {
		t.Fatalf("QQ did not reuse the App transcript: %+v, %v", mapping, err)
	}
	ref, err := s.turnContext(sess, "companion", appID, "你好")(context.Background(), probe.StepContext{RoleID: "companion", SessionID: appID})
	if err != nil || !strings.Contains(ref.Reference, "approved memory marker") || strings.Contains(ref.Reference, "pending memory marker") {
		t.Fatalf("incorrect QQ memory reference: %s, %v", ref.Reference, err)
	}
	content, images := q.prepareAttachments(context.Background(), "companion", appID, "看看", []channel.Attachment{
		{URL: cdn.URL + "/sticker", ContentType: "file", Filename: "sticker.gif"},
		{URL: cdn.URL + "/text", ContentType: "text/plain", Filename: "notes.txt"},
		{URL: cdn.URL + "/unsupported", Filename: "notes.pdf"},
	}, true)
	if len(images) != 1 || images[0].MIMEType != "image/png" || !strings.Contains(content, "QQ attachment marker") || !strings.Contains(content, "当前无法读取此格式的正文") {
		t.Fatalf("unexpected QQ attachments: %d images, %s", len(images), content)
	}
}
