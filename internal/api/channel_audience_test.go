package api

import (
	"os"
	"testing"

	channelstore "chat-agent/internal/channel"
)

func TestGroupAudienceIsIndependentOfLiveMapping(t *testing.T) {
	server := &Server{config: Config{DataDir: t.TempDir()}}
	key := channelstore.ConversationKey{Platform: "qq-official", AccountID: "bot", ConversationKind: "group", ConversationID: "group-1", RoleID: "companion"}
	if err := server.markChannelAudience(key, "old_group_session"); err != nil {
		t.Fatal(err)
	}
	if !server.publicChannelSession("old_group_session") {
		t.Fatal("group privacy floor was not recorded")
	}
	// Moving the provider mapping only marks the destination; the old transcript
	// remains public-scoped and cannot later regain owner memory or tools.
	if err := server.markChannelAudience(key, "new_group_session"); err != nil {
		t.Fatal(err)
	}
	reopened := &Server{config: Config{DataDir: server.config.DataDir}}
	if !reopened.publicChannelSession("old_group_session") || !reopened.publicChannelSession("new_group_session") {
		t.Fatal("group privacy floor did not survive restart or mapping movement")
	}
	if reopened.publicChannelSession("private_session") {
		t.Fatal("ordinary app session was classified as public")
	}
	if err := os.WriteFile(reopened.audiencePath("damaged_session"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if !reopened.publicChannelSession("damaged_session") {
		t.Fatal("damaged privacy scope did not fail closed")
	}
}
