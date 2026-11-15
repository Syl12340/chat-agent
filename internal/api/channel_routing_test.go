package api

import (
	"context"
	"testing"

	channelstore "chat-agent/internal/channel"
)

func TestQQOfficialDoesNotDeliverAppTurnFromLinkedSession(t *testing.T) {
	store, err := channelstore.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := channelstore.ConversationKey{Platform: "qq-official", AccountID: "app", ConversationKind: "private", ConversationID: "chat", RoleID: "companion"}
	if _, _, err = store.ProvisionMapping(key, "linked_session"); err != nil {
		t.Fatal(err)
	}
	if err = store.ActivateMapping(key, "linked_session"); err != nil {
		t.Fatal(err)
	}
	integration := &qqOfficialIntegration{store: store}
	if err = integration.TurnCompleted(context.Background(), TurnCompletion{SessionID: "linked_session", RequestID: "app_request", Text: "只应留在应用"}); err != nil {
		t.Fatal(err)
	}
	if record, ok, err := store.ClaimPending(); err != nil || ok {
		t.Fatalf("app turn created QQ delivery: %+v ok=%v err=%v", record, ok, err)
	}
}
