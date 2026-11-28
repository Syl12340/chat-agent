package courseware

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The store used to keep only the extracted text, so the file a reader was
// looking at could not be shown again and a later extractor had nothing real to
// run on. The original bytes must survive the upload.
func TestAddKeepsTheOriginalBytes(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("# 极限\n\n先理解邻域，再使用定义。\n")
	doc, err := store.Add("companion", "limits.md", "text/markdown", raw)
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join(dir, "source", doc.ID+".md"))
	if err != nil {
		t.Fatalf("original bytes were not stored: %v", err)
	}
	if !bytes.Equal(source, raw) {
		t.Fatalf("stored original differs from the upload:\n got %q\nwant %q", source, raw)
	}
}

func TestDeleteRemovesTheOriginalToo(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := store.Add("companion", "limits.md", "text/markdown", []byte("# 极限\n正文。"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("companion", doc.ID, doc.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "source", doc.ID+".md")); !os.IsNotExist(err) {
		t.Fatalf("delete left the original behind: %v", err)
	}
}

func TestSourceExtRejectsUnsafeNames(t *testing.T) {
	cases := map[string]string{
		"lecture.pdf":      ".pdf",
		"Lecture.PPTX":     ".pptx",
		"notes":            ".bin",
		"weird.t..xt":      ".xt",
		"a.b/c":            ".bin",
		"name.":            ".bin",
	}
	for name, want := range cases {
		if got := sourceExt(name); got != want {
			t.Errorf("sourceExt(%q) = %q, want %q", name, got, want)
		}
	}
}
