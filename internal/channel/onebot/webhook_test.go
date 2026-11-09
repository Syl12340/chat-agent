package onebot

import (
	"errors"
	"testing"
	"time"

	"chat-agent/internal/channel"
)

func TestOwnerPrivateTextNormalization(t *testing.T) {
	body := []byte(`{"time":1794067200,"self_id":10001,"post_type":"message","message_type":"private","sub_type":"friend","message_id":"m-7","user_id":20002,"message":[{"type":"text","data":{"text":"  你好  "}}],"sender":{"nickname":"主人"}}`)
	signature := Signature("ingress-secret", body)
	if !VerifySignature("ingress-secret", body, signature) || VerifySignature("wrong", body, signature) || VerifySignature("ingress-secret", append(body, ' '), signature) {
		t.Fatal("HMAC verification did not bind the exact request body")
	}
	now := time.Date(2026, 11, 8, 1, 2, 3, 0, time.UTC)
	cfg := WebhookConfig{AccountID: "10001", OwnerID: "20002", Now: func() time.Time { return now }}
	envelope, err := Normalize(body, "10001", cfg)
	if err != nil {
		t.Fatal(err)
	}
	wantID, _ := channel.RequestID("10001", "m-7")
	if envelope.EventID != wantID || envelope.Message.Text != "你好" || envelope.Sender.ID != "20002" || envelope.Conversation.Kind != "private" || envelope.PayloadSHA256 != channel.PayloadDigest(body) || !envelope.ReceivedAt.Equal(now) {
		t.Fatalf("unexpected envelope: %#v", envelope)
	}
	again, err := Normalize(body, "10001", cfg)
	if err != nil || again.EventID != envelope.EventID {
		t.Fatal("webhook retry changed deterministic identity")
	}
}

func TestRejectsUntrustedOrUnsupportedOneBotEvents(t *testing.T) {
	cfg := WebhookConfig{AccountID: "10001", OwnerID: "20002"}
	guest := []byte(`{"self_id":10001,"post_type":"message","message_type":"private","sub_type":"friend","message_id":8,"user_id":30003,"message":"hi"}`)
	if _, err := Normalize(guest, "10001", cfg); !errors.Is(err, ErrIgnoredEvent) {
		t.Fatalf("guest error = %v", err)
	}
	image := []byte(`{"self_id":10001,"post_type":"message","message_type":"private","sub_type":"friend","message_id":8,"user_id":20002,"message":[{"type":"image","data":{"file":"x"}}]}`)
	if _, err := Normalize(image, "10001", cfg); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported error = %v", err)
	}
	if _, err := Normalize(image, "wrong-self", cfg); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("self header error = %v", err)
	}
}
