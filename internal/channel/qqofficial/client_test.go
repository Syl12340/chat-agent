package qqofficial

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func testClient(t *testing.T, groups, groupAll bool) *Client {
	t.Helper()
	client, err := New(Config{AppID: "app-1", Secret: "1234567890abcdef", StateDir: t.TempDir(), AllowGroups: groups, AllowGroupAll: groupAll, Now: func() time.Time { return time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestNormalizeC2CUsesOpenIDAndStableMessageIndex(t *testing.T) {
	client := testClient(t, false, false)
	raw := json.RawMessage(`{"id":"message-1","content":" hello ","timestamp":"2026-11-15T11:59:00Z","author":{"user_openid":"user-1"},"message_scene":{"ext":["msg_idx=2","ref_msg_idx=1"]}}`)
	one, err := client.normalize(payload{T: "C2C_MESSAGE_CREATE", D: raw})
	if err != nil {
		t.Fatal(err)
	}
	two, err := client.normalize(payload{T: "C2C_MESSAGE_CREATE", D: raw})
	if err != nil {
		t.Fatal(err)
	}
	if one.EventID != two.EventID || one.Conversation.ID != "user-1" || one.Message.Text != "hello" || one.Message.ReplyTo != "1" {
		t.Fatalf("unexpected normalized envelope: %#v", one)
	}
}

func TestNormalizeGroupRequiresExplicitPolicy(t *testing.T) {
	raw := json.RawMessage(`{"id":"message-2","content":"hi","group_openid":"group-1","author":{"member_openid":"member-1"}}`)
	if _, err := testClient(t, false, false).normalize(payload{T: "GROUP_AT_MESSAGE_CREATE", D: raw}); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("disabled group error = %v", err)
	}
	if _, err := testClient(t, true, false).normalize(payload{T: "GROUP_MESSAGE_CREATE", D: raw}); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("full group error = %v", err)
	}
	envelope, err := testClient(t, true, false).normalize(payload{T: "GROUP_AT_MESSAGE_CREATE", D: raw})
	if err != nil || envelope.Conversation.Kind != "group" || envelope.Conversation.ID != "group-1" {
		t.Fatalf("group mention = %#v, %v", envelope, err)
	}
}
