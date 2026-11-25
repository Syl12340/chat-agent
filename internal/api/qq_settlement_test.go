package api

import (
	"context"
	"testing"
	"time"

	"chat-agent/internal/channel"
	"chat-agent/internal/probe"
)

// qqEnvelope builds one owner private message. Every field Validate() cares
// about is set, so a failure here is about the behaviour under test and not
// about the fixture.
func qqEnvelope(eventID, text, digest string) channel.Envelope {
	now := time.Now().UTC()
	return channel.Envelope{
		Version:       channel.ContractVersion,
		EventID:       eventID,
		Platform:      "qq-official",
		Adapter:       "qq",
		AccountID:     "bot",
		Conversation:  channel.Conversation{Kind: "private", ID: "owner-openid"},
		Sender:        channel.Sender{ID: "owner-openid"},
		Message:       channel.Message{ID: "m-" + eventID, Text: text},
		OccurredAt:    now,
		ReceivedAt:    now,
		PayloadSHA256: channel.PayloadDigest([]byte(digest)),
	}
}

func qqTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := probe.DefaultConfig()
	cfg.Demo = true
	s, err := New(Config{Agent: cfg, DataDir: t.TempDir(), AccessToken: "qq-settlement-test-access-token-32+"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	code, _, err := s.identities.Issue(ownerPrincipalID, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.identities.Consume(code, "qq-official", "bot", "owner-openid"); err != nil {
		t.Fatal(err)
	}
	return s
}

func qqTestIntegration(t *testing.T, s *Server) (*qqOfficialIntegration, *channel.Store) {
	t.Helper()
	store, err := channel.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	q := &qqOfficialIntegration{server: s, store: store, config: QQOfficialConfig{RoleID: "companion"}}
	s.qqofficial = q
	t.Cleanup(func() { s.qqofficial = nil })
	return q, store
}

// The acceptance case: one QQ message settles exactly once. QQ retries delivery
// on its own schedule, so the same event id arrives again - it must not produce a
// second turn.
func TestQQRepeatedEventSettlesOnce(t *testing.T) {
	s := qqTestServer(t)
	q, store := qqTestIntegration(t, s)
	ctx := context.Background()
	envelope := qqEnvelope("evt-repeat", "你好", "payload-one")

	if err := q.receive(ctx, envelope); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	record, duplicate, err := store.Accept(envelope, envelope.EventID)
	if err != nil || !duplicate || record.State == "accepted" {
		t.Fatalf("first delivery must move the inbox past accepted: state=%q duplicate=%v err=%v", record.State, duplicate, err)
	}
	mapping, _, err := q.ensureSession(envelope)
	if err != nil {
		t.Fatal(err)
	}
	before, _, err := s.turns.List(mapping.SessionID)
	if err != nil {
		t.Fatal(err)
	}

	if err := q.receive(ctx, envelope); err != nil {
		t.Fatalf("repeat delivery must not error: %v", err)
	}
	after, _, err := s.turns.List(mapping.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("a repeated event created another turn: %d -> %d", len(before), len(after))
	}
}

// A different payload under the same event id is a conflict, not a retry: it
// must be refused rather than silently replacing or duplicating the first one.
func TestQQSameEventDifferentPayloadIsRefused(t *testing.T) {
	s := qqTestServer(t)
	q, _ := qqTestIntegration(t, s)
	ctx := context.Background()
	if err := q.receive(ctx, qqEnvelope("evt-conflict", "你好", "payload-one")); err != nil {
		t.Fatal(err)
	}
	if err := q.receive(ctx, qqEnvelope("evt-conflict", "你好", "payload-two")); err == nil {
		t.Fatal("a changed payload under a settled event id must be refused")
	}
}

// The acceptance case: unbinding stops delivery. The gate that decides whether a
// private message is allowed through is identity resolution, so revoking the
// identity must make the sender a stranger again - and a stranger never reaches a
// turn. The reply prompting them to bind is not exercised here because that needs
// a live gateway; what matters is that the door is shut.
func TestQQRevokedIdentityNoLongerAdmitsMessages(t *testing.T) {
	s := qqTestServer(t)
	envelope := qqEnvelope("evt-unbound", "你好", "payload-unbound")

	if _, bound, err := s.bindChannelIdentity(envelope); err != nil || !bound {
		t.Fatalf("the bound sender must be admitted: bound=%v err=%v", bound, err)
	}
	identity, ok := s.identities.Resolve("qq-official", "bot", "owner-openid")
	if !ok {
		t.Fatal("fixture identity was not bound")
	}
	if _, err := s.identities.Revoke(identity.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.identities.Resolve("qq-official", "bot", "owner-openid"); ok {
		t.Fatal("a revoked identity still resolves")
	}
	// A plain message is no longer a link command either, so it cannot re-bind.
	if _, bound, err := s.bindChannelIdentity(envelope); bound || err != nil {
		t.Fatalf("a revoked sender must not be admitted again: bound=%v err=%v", bound, err)
	}
}

