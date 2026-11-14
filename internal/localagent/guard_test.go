package localagent

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteRequiresCurrentHashAndDoesNotClobberOnConflict(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.md")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	guard, err := OpenGuard(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	digest := sha256.Sum256([]byte("before"))
	if err = guard.Write("note.md", "after", fmt.Sprintf("%x", digest[:]), "op_1234"); err != nil {
		t.Fatal(err)
	}
	if err = guard.Write("note.md", "clobbered", fmt.Sprintf("%x", digest[:]), "op_5678"); err == nil {
		t.Fatal("stale hash write succeeded")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "after" {
		t.Fatalf("conflict changed file: %q", raw)
	}
}
