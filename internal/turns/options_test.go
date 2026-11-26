package turns

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"chat-agent/internal/media"
)

func TestAppOriginRemainsLegacyCompatibleJSON(t *testing.T) {
	raw, err := json.Marshal(Turn{ID: "app"})
	if err != nil || strings.Contains(string(raw), `"origin"`) {
		t.Fatalf("zero app origin changed legacy JSON: %s err=%v", raw, err)
	}
}

func TestChannelToolTurnKeepsAndRepairsLedgerVersion(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	const session = "shared"
	origin := TurnOrigin{Kind: "channel", Platform: "qq-official", AccountID: "bot", ConversationKind: "private", ConversationID: "conversation", SenderID: "owner"}
	if _, _, err := store.EnqueueWithOrigin(session, "qq-turn", "Question", "auto", "", "", nil, origin); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Begin(session, 0, func(Turn) bool { return true }); err != nil {
		t.Fatal(err)
	}
	tools := []ToolEvent{{ID: "call", Name: "clock", Status: "completed"}}
	if err := store.ProgressWithReasoning(session, "qq-turn", "Reply", "", tools); err != nil {
		t.Fatal(err)
	}
	if got := store.cache[session].Version; got != 5 {
		t.Fatalf("tool progress downgraded the ledger to v%d", got)
	}
	if err := store.Finish(session, "qq-turn", "completed", "Reply", "", 2, tools); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, session+".json")
	read := func() state {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var saved state
		if err := json.Unmarshal(raw, &saved); err != nil {
			t.Fatal(err)
		}
		return saved
	}
	saved := read()
	if saved.Version != 5 {
		t.Fatalf("tool settlement downgraded the ledger to v%d", saved.Version)
	}
	saved.Version = 4
	raw, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	items, paused, err := reopened.List(session)
	if err != nil || paused || len(items) != 1 || items[0].Origin != origin || items[0].Status != "completed" || len(items[0].Tools) != 1 {
		t.Fatalf("reading damaged ledger lost turn metadata: %+v, %v", items, err)
	}
	if got := read().Version; got != 5 {
		t.Fatalf("repair was not persisted: v%d", got)
	}
}

func TestMultimodalQueueLifecycle(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	image := media.Image{ID: "0123456789abcdef0123456789abcdef", MIMEType: "image/png", Size: 4, Width: 1, Height: 1}
	_, _, err = s.EnqueueWithOptions("session", "first", "", "auto", "ecnu-max", "high", []media.Image{image})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.EnqueueWithOptions("session", "first", "", "auto", "ecnu-max", "ultra", []media.Image{image}); !errors.Is(err, ErrConflict) {
		t.Fatal("different options reused ID")
	}
	if _, duplicate, err := s.EnqueueWithOptions("session", "first", "", "auto", "ecnu-max", "high", []media.Image{image}); err != nil || !duplicate {
		t.Fatal("same options not idempotent")
	}
	withHash := image
	withHash.SHA256 = strings.Repeat("a", 64)
	if _, duplicate, err := s.EnqueueWithOptions("session", "first", "", "auto", "ecnu-max", "high", []media.Image{withHash}); err != nil || !duplicate {
		t.Fatal("adding an immutable image hash broke an old request identity")
	}
	if _, _, err = s.EnqueueWithOptions("session", "second", "next", "auto", "ecnu-plus", "high", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.EnqueueWithOptions("session", "third", "last", "auto", "ecnu-plus", "high", nil); err != nil {
		t.Fatal(err)
	}
	batch, err := s.Begin("session", 0, func(Turn) bool { return true })
	if err != nil || len(batch) != 1 {
		t.Fatal("different model was merged")
	}
	batch[0].Images[0].ID = "mutated"
	if err = s.Finish("session", "first", "completed", "reply", "", 2, nil); err != nil {
		t.Fatal(err)
	}
	batch, err = s.Begin("session", 2, func(Turn) bool { return true })
	if err != nil || len(batch) != 2 {
		t.Fatal("matching selections were not merged")
	}
	if err = s.Finish("session", "second", "completed", "reply", "", 5, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.RemoveBatch("session", "third"); err != nil {
		t.Fatal(err)
	}
	items, _, err := s.List("session")
	if err != nil || len(items) != 1 || items[0].Images[0].ID != image.ID {
		t.Fatal("batch deletion or cloning failed")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	items, _, err = s.List("session")
	if err != nil || len(items) != 1 || items[0].Model != "ecnu-max" || items[0].Thinking != "high" {
		t.Fatal("selection did not survive reopen")
	}
	images, err := s.AllImages("session")
	if err != nil || len(images) != 1 || images[0].ID != image.ID {
		t.Fatal("full image reference list failed")
	}
}

func TestQueueDoesNotMergeDifferentChannelAudiences(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	app := TurnOrigin{}
	qq := TurnOrigin{Kind: "channel", Platform: "qq-official", AccountID: "app", ConversationKind: "private", ConversationID: "chat", SenderID: "owner"}
	if _, _, err = s.EnqueueWithOrigin("session", "app_turn", "应用消息", "auto", "", "", nil, app); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.EnqueueWithOrigin("session", "qq_turn", "QQ 消息", "auto", "", "", nil, qq); err != nil {
		t.Fatal(err)
	}
	batch, err := s.Begin("session", 0, func(Turn) bool { return true })
	if err != nil || len(batch) != 1 || batch[0].ID != "app_turn" {
		t.Fatalf("different audiences merged: %+v err=%v", batch, err)
	}
	if err = s.Finish("session", "app_turn", "completed", "reply", "", 2, nil); err != nil {
		t.Fatal(err)
	}
	batch, err = s.Begin("session", 2, func(Turn) bool { return true })
	if err != nil || len(batch) != 1 || batch[0].Origin != qq {
		t.Fatalf("channel origin did not survive queue: %+v err=%v", batch, err)
	}
}
