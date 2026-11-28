package courseware

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The defect behind the three-phase Add: a file this extractor cannot read used to
// be refused, and the bytes the person handed over were thrown away with it. The
// upload is now stored and its state says why there is no text.
func TestAddKeepsAnUnreadableOriginal(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Text drawn only through a font encoding: real page content, nothing this
	// reader can decode.
	raw := buildPDF(t, "BT /F1 12 Tf <002A0044> Tj ET")
	doc, err := store.Add("companion", "lecture.pdf", "application/pdf", raw)
	if err != nil {
		t.Fatalf("an unreadable file must still be stored: %v", err)
	}
	if doc.Status() != extractionFailed {
		t.Fatalf("status = %q, want failed", doc.Status())
	}
	if doc.TextBytes != 0 {
		t.Fatalf("a failed extraction must not claim text: %d", doc.TextBytes)
	}
	if len(doc.Warnings) == 0 {
		t.Fatal("a failed extraction must say why")
	}
	stored, err := os.ReadFile(filepath.Join(dir, "source", doc.ID+".pdf"))
	if err != nil {
		t.Fatalf("the original was not kept: %v", err)
	}
	if string(stored) != string(raw) {
		t.Fatal("stored original differs from the upload")
	}
	// Reading the projection is a conflict, not an empty lecture.
	if _, _, _, err := store.Read("companion", doc.ID, "", 0, 4000); !errors.Is(err, ErrTextUnavailable) {
		t.Fatalf("read of a failed document = %v, want ErrTextUnavailable", err)
	}
}

// The reported bug: a document with one readable page and one encoded page used to
// be stored as if it were complete.
func TestAddRecordsAPartialExtraction(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw := buildPDF(t, "BT /F1 12 Tf (Readable first page) Tj ET\nBT /F1 12 Tf <002A0044> Tj ET")
	doc, err := store.Add("companion", "mixed.pdf", "application/pdf", raw)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Status() != extractionLimited {
		t.Fatalf("status = %q, want limited", doc.Status())
	}
	if !doc.Incomplete() {
		t.Fatal("a limited extraction is incomplete by definition")
	}
	if len(doc.Warnings) == 0 {
		t.Fatal("a limited extraction must say what is missing")
	}
	// Reloading must keep the state: it is stored, not derived per request.
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("a limited record must still load: %v", err)
	}
	docs, err := reopened.List("companion")
	if err != nil || len(docs) != 1 {
		t.Fatalf("reload lost the document: %v %d", err, len(docs))
	}
	if docs[0].Status() != extractionLimited || len(docs[0].Warnings) == 0 {
		t.Fatalf("status did not survive a reload: %+v", docs[0])
	}
}

// The bytes served have to be the bytes recorded, or the page shown is not the
// page the text came from.
func TestSourceRejectsChangedBytes(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := store.Add("companion", "limits.md", "text/markdown", []byte("# 极限\n正文。"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "source", doc.ID+".md")
	if err := os.WriteFile(path, []byte("# 极限\n被换掉的内容。"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Source("companion", doc.ID); err == nil {
		t.Fatal("a swapped original must be refused")
	}
}

// Records written before originals were kept cannot be opened; that is different
// from the document being gone.
func TestSourceReportsAMissingOriginal(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := store.Add("companion", "limits.md", "text/markdown", []byte("# 极限\n正文。"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "source", doc.ID+".md")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Source("companion", doc.ID); !errors.Is(err, ErrSourceMissing) {
		t.Fatalf("missing original = %v, want ErrSourceMissing", err)
	}
}

// A record whose status and text disagree is damage, not a state to serve.
func TestOpenRejectsAnInconsistentRecord(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add("companion", "limits.md", "text/markdown", []byte("正文。")); err != nil {
		t.Fatal(err)
	}
	// Claim failure while still carrying text.
	index := filepath.Join(dir, "index.json")
	raw, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(raw), `"extraction_status":"complete"`, `"extraction_status":"failed"`, 1)
	if broken == string(raw) {
		t.Fatalf("test did not find the status to break: %s", raw)
	}
	if err := os.WriteFile(index, []byte(broken), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("an inconsistent record must not load")
	}
}
