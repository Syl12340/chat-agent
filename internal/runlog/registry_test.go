package runlog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"chat-agent/internal/probe"
	"chat-agent/internal/turns"
)

func writeLegacyHistory(t *testing.T, root, id string) {
	t.Helper()
	dir := filepath.Join(root, probe.AgentID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("{\"version\":1,\"role_id\":\"companion\"}\n" +
		"{\"version\":2,\"messages\":[{\"role\":\"user\",\"content\":\"hello\"},{\"role\":\"assistant\",\"content\":\"hi\",\"reasoning_content\":\"display only\"}]}\n")
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationRestartProjection(t *testing.T) {
	root := t.TempDir()
	id := "migration-one"
	writeLegacyHistory(t, root, id)
	legacyTurns, err := turns.Open(filepath.Join(root, "turns"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = legacyTurns.Enqueue(id, "request-one", "queued question", "auto"); err != nil {
		t.Fatal(err)
	}
	if _, err = legacyTurns.Begin(id, 2, func(turns.Turn) bool { return true }); err != nil {
		t.Fatal(err)
	}
	if err = legacyTurns.Close(); err != nil {
		t.Fatal(err)
	}

	registry, err := OpenRegistry(filepath.Join(root, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	report, err := registry.MigrateLegacy(root)
	if err != nil || report.Migrated != 1 {
		t.Fatalf("migration = %+v, %v", report, err)
	}
	if err = registry.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, probe.AgentID, id+".jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy history remained: %v", err)
	}

	registry, err = OpenRegistry(filepath.Join(root, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	historyPath, err := registry.HistoryPath(id)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := probe.OpenRuntimeSession(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if messages := sess.Messages(); len(messages) != 2 || messages[1].ReasoningContent != "display only" {
		t.Fatalf("history projection lost data: %+v", messages)
	}
	if err = sess.Close(); err != nil {
		t.Fatal(err)
	}
	ledger, err := turns.OpenResolved(filepath.Join(root, "turns"), registry.TurnsPath)
	if err != nil {
		t.Fatal(err)
	}
	items, paused, err := ledger.List(id)
	if err != nil || !paused || len(items) != 1 || items[0].Status != "interrupted" {
		t.Fatalf("turn projection = %+v paused=%v err=%v", items, paused, err)
	}
	if err = ledger.Close(); err != nil {
		t.Fatal(err)
	}
	exportDir := filepath.Join(t.TempDir(), "legacy-export")
	if err = registry.ExportLegacy(root, exportDir); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(exportDir, probe.AgentID, id+".jsonl")); err != nil {
		t.Fatalf("compatibility history export missing: %v", err)
	}
	if _, err = os.Stat(filepath.Join(exportDir, "turns", id+".json")); err != nil {
		t.Fatalf("compatibility turn export missing: %v", err)
	}
}

func TestHardDeleteRewritesGeneration(t *testing.T) {
	root := t.TempDir()
	id := "delete-one"
	registry, err := OpenRegistry(filepath.Join(root, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	if err = registry.Create(id, "companion"); err != nil {
		t.Fatal(err)
	}
	historyPath, _ := registry.HistoryPath(id)
	sess, err := probe.OpenRuntimeSession(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	c := probe.DefaultConfig()
	c.Demo = true
	c.Model = "demo"
	agent, err := probe.New(c, sess)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := turns.OpenResolved(filepath.Join(root, "turns"), registry.TurnsPath)
	if err != nil {
		t.Fatal(err)
	}
	for n, rid := range []string{"request-one", "request-two"} {
		if _, _, err = ledger.Enqueue(id, rid, "hello", "auto"); err != nil {
			t.Fatal(err)
		}
		batch, beginErr := ledger.Begin(id, n*2, func(turns.Turn) bool { return true })
		if beginErr != nil || len(batch) != 1 {
			t.Fatalf("begin = %+v, %v", batch, beginErr)
		}
		if err = probe.Run(context.Background(), agent, time.Second, "hello", nil); err != nil {
			t.Fatal(err)
		}
		if err = ledger.Finish(id, rid, "completed", "reply", "", (n+1)*2, nil); err != nil {
			t.Fatal(err)
		}
	}
	oldGeneration := filepath.Dir(historyPath)
	if err = os.WriteFile(filepath.Join(oldGeneration, "snapshots", "derived.json"), []byte("deleted dependency"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = sess.Close(); err != nil {
		t.Fatal(err)
	}
	if err = ledger.Close(); err != nil {
		t.Fatal(err)
	}

	removed, deleteID, err := registry.RewriteLatestTurn(id, "request-two", 2)
	if err != nil || len(removed) != 1 || removed[0] != "request-two" {
		t.Fatalf("rewrite removed=%v delete=%q err=%v", removed, deleteID, err)
	}
	if _, err = os.Stat(oldGeneration); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old generation or snapshots remained: %v", err)
	}
	historyPath, err = registry.HistoryPath(id)
	if err != nil {
		t.Fatal(err)
	}
	sess, err = probe.OpenRuntimeSession(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(sess.Messages()); got != 2 {
		t.Fatalf("deleted messages returned after rewrite: %d", got)
	}
	if err = sess.Close(); err != nil {
		t.Fatal(err)
	}
	ledger, err = turns.OpenResolved(filepath.Join(root, "turns"), registry.TurnsPath)
	if err != nil {
		t.Fatal(err)
	}
	items, _, err := ledger.List(id)
	if err != nil || len(items) != 1 || items[0].ID != "request-one" {
		t.Fatalf("remaining turns = %+v, %v", items, err)
	}
	if err = ledger.Close(); err != nil {
		t.Fatal(err)
	}

	wholeDeleteID, err := registry.BeginSessionDeletion(id)
	if err != nil {
		t.Fatal(err)
	}
	if err = registry.RemoveSessionData(root, id); err != nil {
		t.Fatal(err)
	}
	if err = registry.CompleteDeletion(wholeDeleteID); err != nil {
		t.Fatal(err)
	}
	if _, err = registry.HistoryPath(id); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted session remained readable: %v", err)
	}
	if err = registry.Create(id, "companion"); err == nil {
		t.Fatal("deleted session id was reused")
	}
}

func TestMigrationSwitchFailureIsExplicit(t *testing.T) {
	root := t.TempDir()
	id := "switch-one"
	writeLegacyHistory(t, root, id)
	registry, err := OpenRegistry(filepath.Join(root, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	registry.switchHook = func(stage string) error {
		if stage == "after-index" {
			return errors.New("injected switch failure")
		}
		return nil
	}
	if _, err = registry.MigrateLegacy(root); err == nil {
		t.Fatal("migration unexpectedly survived injected switch failure")
	}
	if _, err = os.Stat(filepath.Join(root, probe.AgentID, id+".jsonl")); err != nil {
		t.Fatalf("legacy authority was destroyed before publish: %v", err)
	}
	if _, err = registry.HistoryPath(id); !errors.Is(err, ErrMaintenance) {
		t.Fatalf("reader did not expose maintenance state: %v", err)
	}
	registry.switchHook = nil
	report, err := registry.MigrateLegacy(root)
	if err != nil || report.Resumed != 1 {
		t.Fatalf("resume = %+v, %v", report, err)
	}
	if _, err = registry.HistoryPath(id); err != nil {
		t.Fatalf("resumed runtime is unreadable: %v", err)
	}
	if _, err = os.Stat(filepath.Join(root, probe.AgentID, id+".jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy source remained after resume: %v", err)
	}
}
