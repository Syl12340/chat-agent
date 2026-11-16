package companion

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReplyRhythmUpdatesAndLegacySnapshotDefaults(t *testing.T) {
	dir := t.TempDir()
	legacy := Snapshot{Version: 1, RoleID: "companion", Revision: 1, Preferences: Preferences{ReplyLength: "default", Tone: "default", Advice: "default", Questions: "default"}}
	raw, _ := json.Marshal(legacy)
	if err := os.WriteFile(filepath.Join(dir, "companion.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	loaded, err := store.Get("companion")
	if err != nil || loaded.Preferences.ReplyRhythm != "default" {
		t.Fatalf("legacy load = %+v, %v", loaded, err)
	}
	single := "single"
	updated, err := store.Update("companion", Patch{Revision: 1, ReplyRhythm: &single})
	if err != nil || updated.Preferences.ReplyRhythm != "single" || updated.Revision != 2 {
		t.Fatalf("updated = %+v, %v", updated, err)
	}
}
