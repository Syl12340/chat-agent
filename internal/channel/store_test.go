package channel

import (
	"errors"
	"testing"
	"time"
)

func TestStoreDedupesAndDoesNotReplayUncertainDelivery(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	envelope := Envelope{Version: ContractVersion, EventID: "event-one", Platform: "onebot-v11", Adapter: "test", AccountID: "10001", Conversation: Conversation{Kind: "private", ID: "20002"}, Sender: Sender{ID: "20002"}, Message: Message{ID: "7", Text: "hello"}, OccurredAt: now, ReceivedAt: now, PayloadSHA256: PayloadDigest([]byte("one"))}
	if _, duplicate, err := store.Accept(envelope, "event-one"); err != nil || duplicate {
		t.Fatalf("accept duplicate=%v err=%v", duplicate, err)
	}
	if _, duplicate, err := store.Accept(envelope, "event-one"); err != nil || !duplicate {
		t.Fatalf("retry duplicate=%v err=%v", duplicate, err)
	}
	conflict := envelope
	conflict.PayloadSHA256 = PayloadDigest([]byte("different"))
	if _, _, err := store.Accept(conflict, "event-one"); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict error = %v", err)
	}
	key := ConversationKey{Platform: "onebot-v11", AccountID: "10001", ConversationKind: "private", ConversationID: "20002", RoleID: "companion"}
	mapping, created, err := store.ProvisionMapping(key, "channel-session")
	if err != nil || !created || mapping.State != "provisioning" {
		t.Fatalf("mapping=%#v created=%v err=%v", mapping, created, err)
	}
	if err := store.ActivateMapping(key, mapping.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetInboxState(envelope.EventID, "queued", mapping.SessionID); err != nil {
		t.Fatal(err)
	}
	outbox, duplicate, err := store.SettleCompletion(mapping.SessionID, envelope.EventID, "20002", "reply", []string{envelope.EventID})
	if err != nil || duplicate || outbox.State != "pending" {
		t.Fatalf("outbox=%#v duplicate=%v err=%v", outbox, duplicate, err)
	}
	claimed, ok, err := store.ClaimPending()
	if err != nil || !ok || claimed.State != "sending" {
		t.Fatalf("claim=%#v ok=%v err=%v", claimed, ok, err)
	}
	attention := envelope
	attention.EventID = "event-two"
	attention.Message.ID = "8"
	attention.PayloadSHA256 = PayloadDigest([]byte("two"))
	if _, _, err := store.Accept(attention, attention.EventID); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, ok, err := reopened.ClaimPending(); err != nil || ok {
		t.Fatalf("uncertain delivery was replayable: ok=%v err=%v", ok, err)
	}
	reopened.mu.Lock()
	state := reopened.state.Outbox[claimed.DeliveryID].State
	inboxState := reopened.state.Inbox[envelope.EventID].State
	attentionState := reopened.state.Inbox[attention.EventID].State
	reopened.mu.Unlock()
	if state != "unknown" || inboxState != "completed" || attentionState != "needs_attention" {
		t.Fatalf("restart state outbox=%s inbox=%s accepted=%s", state, inboxState, attentionState)
	}
}
