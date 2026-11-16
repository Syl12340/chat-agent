package astrbot

import "testing"

func TestDecodeAndSignature(t *testing.T) {
	body := []byte(`{"version":1,"platform":"aiocqhttp","account_id":"bot-1","conversation_kind":"private","conversation_id":"aiocqhttp:FriendMessage:42","sender_id":"42","sender_name":"主人","message_id":"msg-7","text":"你好","occurred_at":"2026-11-16T00:00:00Z"}`)
	secret := "astrbot-bridge-fixture-secret-over-32-characters"
	if !VerifySignature(secret, body, Signature(secret, body)) || VerifySignature(secret, body, Signature(secret+"x", body)) {
		t.Fatal("signature verification mismatch")
	}
	envelope, err := Decode(body)
	if err != nil || envelope.Adapter != "astrbot" || envelope.Conversation.ID != "aiocqhttp:FriendMessage:42" || envelope.EventID == "" {
		t.Fatalf("decode: %#v %v", envelope, err)
	}
	second, err := Decode(body)
	if err != nil || second.EventID != envelope.EventID {
		t.Fatal("retry identity is not stable")
	}
}
