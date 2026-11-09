package memory_test

import (
	"path/filepath"
	"testing"
	"time"

	"chat-agent/internal/companion"
	"chat-agent/internal/memory"
	"chat-agent/internal/retrieval"
)

// A single offline flow checks the retrieval contract rather than a phrase
// matrix: subject overlap, review/role isolation and live correction/deletion.
func TestReferenceRetrievalLifecycle(t *testing.T) {
	root := t.TempDir()
	store, err := memory.Open(filepath.Join(root, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	add := func(role, content, status string) memory.Entry {
		t.Helper()
		e, err := store.Create(role, content, status, "fixture", "")
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	fact := add("companion", "我喜欢跑步，平时周末去公园。", "approved")
	add("companion", "今天想聊聊天，谢谢你陪我。", "approved")
	add("companion", "我的跑步秘密记录。", "pending")
	other := add("planner", "跑步安排只属于另一个角色。", "approved")
	find := func(query string) []memory.Entry {
		t.Helper()
		entries, err := store.Search("companion", query, 5)
		if err != nil {
			t.Fatal(err)
		}
		return entries
	}
	got := find("今天我想聊聊跑步，你还记得吗？")
	if len(got) != 1 || got[0].ID != fact.ID {
		t.Fatalf("subject retrieval admitted filler or private records: %+v", got)
	}
	if len(find("今天我们聊聊")) != 0 {
		t.Fatal("conversational filler should not recall arbitrary personal facts")
	}
	if len(find("")) != 2 {
		t.Fatal("explicit empty memory query must retain recent approved listing")
	}
	pending, err := store.Find("companion", "跑步", "pending", 5)
	if err != nil || len(pending) != 1 || pending[0].Status != "pending" {
		t.Fatal("explicit candidate management must remain available")
	}
	residence := add("companion", "用户住在甲楼", "approved")
	got = find("我住在哪里，先聊聊。")
	if len(got) != 1 || got[0].ID != residence.ID {
		t.Fatal("function-word filtering erased the location-bearing term 住在")
	}
	fact, err = store.Update("companion", fact.ID, "我改成喜欢游泳，周末去泳池。", "approved", fact.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if len(find("跑步")) != 0 {
		t.Fatal("superseded content remained searchable")
	}
	got = find("游泳")
	if len(got) != 1 || got[0].ID != fact.ID {
		t.Fatal("correction was not immediately searchable")
	}
	if err = store.Delete("companion", fact.ID, fact.Revision); err != nil {
		t.Fatal(err)
	}
	if len(find("游泳")) != 0 {
		t.Fatal("forgotten record remained searchable")
	}
	separate, err := store.Search("planner", "跑步", 5)
	if err != nil || len(separate) != 1 || separate[0].ID != other.ID {
		t.Fatal("role isolation changed after deletion")
	}

	life, err := companion.OpenLife(filepath.Join(root, "life"))
	if err != nil {
		t.Fatal(err)
	}
	defer life.Close()
	now := time.Now().UTC()
	soon := now.Add(time.Hour)
	old := now.Add(-365 * 24 * time.Hour)
	episode := func(role, title, status string, at *time.Time) companion.Episode {
		t.Helper()
		e, err := life.AddEpisode(role, companion.EpisodeDraft{Title: title, Content: "面试准备", Status: status, EventAt: at, SourceText: "original-private-evidence"})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	near := episode("companion", "面试", "open", &soon)
	episode("companion", "面试", "resolved", &old)
	episode("companion", "面试", "pending", &soon)
	episode("planner", "面试", "open", &soon)
	if _, err = life.AddEpisode("companion", companion.EpisodeDraft{Title: "今晚看电影", Content: "和朋友去影院", Status: "open", EventAt: &soon}); err != nil {
		t.Fatal(err)
	}
	events, err := life.Context("companion", "面试")
	if err != nil || len(events) != 2 || events[0].ID != near.ID || events[0].SourceText != "" {
		t.Fatalf("event relevance/time/evidence filtering failed: %+v %v", events, err)
	}
	if events, err = life.Context("companion", "你好"); err != nil || len(events) != 0 {
		t.Fatal("unrelated open event leaked into greeting context")
	}
	if events, err = life.Context("companion", ""); err != nil || len(events) != 0 {
		t.Fatal("empty automatic context must not inject events")
	}
	if err = life.DeleteEpisode("companion", near.ID, near.Revision, nil); err != nil {
		t.Fatal(err)
	}
	events, err = life.Context("companion", "面试")
	if err != nil || len(events) != 1 || events[0].ID == near.ID {
		t.Fatal("deleted event remained in automatic context")
	}

	// Whole Latin terms avoid accidental substrings (Go is not mango).
	docs := []retrieval.Document{{ID: "mango", Text: "mango smoothie", UpdatedAt: now}, {ID: "go", Text: "I use Go for a server", UpdatedAt: now}}
	ranked := retrieval.Rank("tell me about Go", docs, 5)
	if len(ranked) != 1 || docs[ranked[0].Index].ID != "go" {
		t.Fatal("Latin search matched a substring")
	}
}
