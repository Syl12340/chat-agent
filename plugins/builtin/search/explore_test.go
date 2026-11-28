package search

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"chat-agent/internal/webtools"
)

func exploreFixture(t *testing.T) (*Plugin, string) {
	t.Helper()
	plugin := &Plugin{pages: newPageStore()}
	page := webtools.ParsePage("https://example.edu/courses", []byte(`<html><head><title>课程目录</title></head><body>
<h1>课程目录</h1>
<p>按学期排列。</p>
<h2>第一学期</h2>
<table><tr><th>课程</th><th>学分</th></tr><tr><td>数学分析</td><td>5</td></tr></table>
<ul>
<li><a href="/lesson/1">第一课 极限</a></li>
<li><a href="/lesson/2">第二课 导数</a></li>
<li><a href="/notice">教务公告</a></li>
</ul>
</body></html>`), false)
	id := plugin.pages.put("", page)
	if id == "" {
		t.Fatal("a page handle should be allocated")
	}
	return plugin, id
}

func readView(t *testing.T, plugin *Plugin, args map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	result, err := plugin.readPage(context.Background(), raw)
	if err != nil {
		t.Fatalf("read %v: %v", args, err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(result.Output), &out); err != nil {
		t.Fatalf("read %v returned unparsable output: %v", args, err)
	}
	return out
}

// The point of a handle is asking one question at a time about the same page.
func TestReadPagesAnswersViewsOfOneObservation(t *testing.T) {
	plugin, id := exploreFixture(t)

	outline := readView(t, plugin, map[string]any{"page_id": id, "view": "outline"})
	if outline["title"] != "课程目录" {
		t.Fatalf("outline title = %v", outline["title"])
	}
	headings, ok := outline["headings"].([]any)
	if !ok || len(headings) == 0 {
		t.Fatalf("outline lost the headings: %v", outline["headings"])
	}

	tables := readView(t, plugin, map[string]any{"page_id": id, "view": "tables"})
	if count, _ := tables["table_count"].(float64); count != 1 {
		t.Fatalf("table count = %v", tables["table_count"])
	}

	links := readView(t, plugin, map[string]any{"page_id": id, "view": "links"})
	if count, _ := links["link_count"].(float64); count != 3 {
		t.Fatalf("link count = %v", links["link_count"])
	}
}

// Filtering is how a model finds the one link it wants on a busy page.
func TestReadLinksCanBeFiltered(t *testing.T) {
	plugin, id := exploreFixture(t)
	out := readView(t, plugin, map[string]any{"page_id": id, "view": "links", "filter": "公告"})
	links, ok := out["links"].([]any)
	if !ok || len(links) != 1 {
		t.Fatalf("filter should narrow to one link, got %v", out["links"])
	}
	first, _ := links[0].(map[string]any)
	if first["url"] != "https://example.edu/notice" {
		t.Fatalf("filtered link url = %v", first["url"])
	}
}

// A long page is read in slices, and the caller is told where to continue.
func TestReadContentPagesThroughTheText(t *testing.T) {
	plugin, id := exploreFixture(t)
	first := readView(t, plugin, map[string]any{"page_id": id, "view": "content", "offset": 0, "limit": 200})
	next, _ := first["next_offset"].(float64)
	more, _ := first["more"].(bool)
	if next <= 0 || !more {
		t.Fatalf("first slice should report where to continue: %v", first)
	}
	second := readView(t, plugin, map[string]any{"page_id": id, "view": "content", "offset": int(next), "limit": 200})
	if second["offset"] == first["offset"] {
		t.Fatalf("second slice must start further in: %v", second["offset"])
	}
	slice, _ := second["content"].(string)
	if strings.TrimSpace(slice) == "" {
		t.Fatal("a continuation slice must carry text")
	}
}

// A handle from another conversation, or one already dropped, is not silently
// re-fetched: the caller named something this conversation does not hold.
func TestUnknownPageHandleIsReported(t *testing.T) {
	plugin := &Plugin{pages: newPageStore()}
	_, err := plugin.readPage(context.Background(), json.RawMessage(`{"page_id":"pdeadbeef","view":"outline"}`))
	if err == nil || !strings.Contains(err.Error(), "again") {
		t.Fatalf("unknown handle = %v, want a message telling the caller to open it again", err)
	}
}

// The per-conversation budget exists so a model that keeps opening pages instead
// of reading them feels the limit.
func TestPageStoreDropsTheOldestHandle(t *testing.T) {
	store := newPageStore()
	page := webtools.Page{URL: "https://example.edu/", Content: "x", Kind: webtools.KindArticle, Parse: "good"}
	first := store.put("s", page)
	if first == "" {
		t.Fatal("expected a handle")
	}
	for i := 0; i < maxPagesPerSession; i++ {
		if store.put("s", page) == "" {
			t.Fatal("expected a handle")
		}
	}
	if _, ok := store.get("s", first); ok {
		t.Fatal("the oldest page should have been dropped")
	}
	if _, ok := store.get("other", first); ok {
		t.Fatal("pages must not leak between conversations")
	}
}
