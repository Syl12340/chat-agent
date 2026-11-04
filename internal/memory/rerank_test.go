package memory

import (
	"path/filepath"
	"strings"
	"testing"
)

type stubReranker struct {
	order []int
	ok    bool
	calls int
}

func (s *stubReranker) Rerank(query string, documents []string, limit int) ([]int, bool) {
	s.calls++
	return s.order, s.ok
}

// An unavailable model must never change what the local ranking already returns.
func TestSearchKeepsLocalOrderWhenRerankerFails(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, content := range []string{"用户喜欢喝美式咖啡", "用户住在上海", "用户在读研究生"} {
		if _, err := store.Create("role", content, "approved", "user", "session"); err != nil {
			t.Fatal(err)
		}
	}
	local, err := store.Search("role", "咖啡", 5)
	if err != nil || len(local) == 0 {
		t.Fatalf("local search failed: %v %d", err, len(local))
	}

	broken := &stubReranker{ok: false}
	store.SetReranker(broken)
	after, err := store.Search("role", "咖啡", 5)
	if err != nil {
		t.Fatal(err)
	}
	if broken.calls == 0 {
		t.Fatal("the reranker was never consulted")
	}
	if len(after) != len(local) || after[0].ID != local[0].ID {
		t.Fatalf("failed reranking changed the local result: %v vs %v", ids(after), ids(local))
	}
}

// A working model reorders and adds recall, but never drops a keyword match.
func TestSearchMergesSemanticAndLocalMatches(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	created := []Entry{}
	for _, content := range []string{"用户住在上海", "用户喜欢喝美式咖啡"} {
		entry, err := store.Create("role", content, "approved", "user", "session")
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, entry)
	}
	if got, err := store.Search("role", "咖啡", 5); err != nil || len(got) != 1 {
		t.Fatalf("baseline keyword search unexpected: %v %d", err, len(got))
	}
	// Pick the candidate the keyword search does NOT match, by the store's own
	// ordering, so the test does not depend on which entry List returns first.
	stored, err := store.List("role")
	if err != nil {
		t.Fatal(err)
	}
	semanticIndex, keywordID := -1, ""
	for i, entry := range stored {
		if strings.Contains(entry.Content, "咖啡") {
			keywordID = entry.ID
			continue
		}
		if semanticIndex < 0 {
			semanticIndex = i
		}
	}
	if semanticIndex < 0 || keywordID == "" {
		t.Fatal("fixture did not produce one matching and one unrelated entry")
	}
	// The model surfaces only the unrelated record; the keyword match must survive.
	store.SetReranker(&stubReranker{ok: true, order: []int{semanticIndex}})
	got, err := store.Search("role", "咖啡", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("semantic layer dropped the keyword match: %v", ids(got))
	}
	if got[0].ID == keywordID {
		t.Fatalf("semantic result should lead, got %v", ids(got))
	}
	found := false
	for _, entry := range got {
		if entry.ID == keywordID {
			found = true
		}
	}
	if !found {
		t.Fatalf("keyword match missing from the merged result: %v", ids(got))
	}
}

func ids(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, entry := range entries {
		out[i] = entry.ID
	}
	return out
}
