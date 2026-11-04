package webtools

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"
)

func base64URL(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

// A model that splits one term into characters must not reach the upstream
// character by character; the repair keeps the term the user actually said.
func TestSearchQueryRepair(t *testing.T) {
	cases := map[string]string{
		"大 肥 鱼":        "大肥鱼",
		"大肥鱼":          "大肥鱼",
		"  大  肥  鱼  ":  "大肥鱼",
		"大肥鱼 是什么":      "大肥鱼 是什么",
		"红烧肉 做法":       "红烧肉 做法",
		"DeepSeek 大肥鱼": "DeepSeek 大肥鱼",
		"  ":           "",
	}
	for raw, want := range cases {
		got, err := SearchQuery(raw)
		if want == "" {
			if err == nil {
				t.Fatalf("SearchQuery(%q) accepted an empty query", raw)
			}
			continue
		}
		if err != nil {
			t.Fatalf("SearchQuery(%q) failed: %v", raw, err)
		}
		if got != want {
			t.Fatalf("SearchQuery(%q) = %q, want %q", raw, got, want)
		}
	}
	if _, err := SearchQuery("鱼"); err == nil {
		t.Fatal("a single-character query must be refused instead of answered with a dictionary page")
	}
	if got, err := SearchQuery("go 语言"); err != nil || got != "go 语言" {
		t.Fatalf("latin query changed: %q %v", got, err)
	}
}

// The engine returns per-character dictionary pages for a CJK name carrying
// extra qualifiers, so a subject-less result set must be retried alone.
func TestCoreTermAndMatch(t *testing.T) {
	cases := map[string]string{
		`"大肥鱼" 昵称 出处 含义`: "大肥鱼",
		"大肥鱼 昵称 出处":      "大肥鱼",
		"deepseek 大肥鱼":   "大肥鱼",
		"go 语言":          "语言",
		"大 肥 鱼":          "大肥鱼",
	}
	for query, want := range cases {
		if got := coreTerm(query); got != want {
			t.Fatalf("coreTerm(%q) = %q, want %q", query, got, want)
		}
	}
	dictionary := []Result{{Title: "大（汉语文字）_百度百科", Snippet: "大"}}
	if matchesCore(dictionary, "大肥鱼") {
		t.Fatal("a dictionary page for 大 must not count as a match for 大肥鱼")
	}
	relevant := []Result{{Title: "为什么叫DeepSeek蓝色大肥鱼？"}}
	if !matchesCore(relevant, "大肥鱼") {
		t.Fatal("a title containing the subject must count as a match")
	}
}

// The results page is real HTML: candidates come from the engine's own markup,
// and the engine's redirect wrapper must be unwrapped to the target URL.
func TestParseResults(t *testing.T) {
	wrapped := "https://www.bing.com/ck/a?!&&p=abc&u=a1" + strings.TrimRight(base64URL("https://example.org/real"), "=")
	page := `
<li class="b_algo"><h2><a href="` + wrapped + `">真正的<b>标题</b></a></h2><p>摘要 &amp; 说明</p></li>
<li class="b_algo"><h2><a href="https://example.org/second">第二条</a></h2></li>
<li class="b_algo"><h2>no link</h2></li>
<li class="b_algo"><h2><a href="https://example.org/real">重复</a></h2></li>
`
	results := parseResults(page)
	if len(results) != 2 {
		t.Fatalf("expected 2 usable candidates, got %d: %+v", len(results), results)
	}
	if results[0].URL != "https://example.org/real" {
		t.Fatalf("engine redirect was not unwrapped: %s", results[0].URL)
	}
	if results[0].Title != "真正的标题" {
		t.Fatalf("inline emphasis was not flattened: %q", results[0].Title)
	}
	if results[0].Snippet != "摘要 & 说明" {
		t.Fatalf("snippet entities were not decoded: %q", results[0].Snippet)
	}
	if results[1].URL != "https://example.org/second" {
		t.Fatalf("second candidate lost: %+v", results[1])
	}
}

// End-to-end: a qualified CJK query whose page yields dictionary results is
// retried with the subject alone; a Latin query is never retried.
func TestSearchRetriesCJKSubject(t *testing.T) {
	client, err := New(Config{Enabled: true, SearchURL: "https://search.example/search"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	seen := []string{}
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		query := req.URL.Query().Get("q")
		seen = append(seen, query)
		title := "大（汉语文字）_百度百科"
		if query == "大肥鱼" {
			title = "为什么叫DeepSeek蓝色大肥鱼？"
		}
		body := `<li class="b_algo"><h2><a href="https://example.org/a">` + title + `</a></h2></li>`
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/html"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})

	results, err := client.Search(context.Background(), `"大肥鱼" 昵称 出处 含义`)
	if err != nil {
		t.Fatalf("qualified search failed: %v", err)
	}
	if len(seen) != 2 || seen[0] != `"大肥鱼" 昵称 出处 含义` || seen[1] != "大肥鱼" {
		t.Fatalf("unexpected engine queries: %q", seen)
	}
	if len(results) != 1 || !strings.Contains(results[0].Title, "大肥鱼") {
		t.Fatalf("fallback did not return the subject's results: %+v", results)
	}

	seen = nil
	if _, err := client.Search(context.Background(), "go docs"); err != nil {
		t.Fatalf("latin search failed: %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("a latin query must not be retried, got %q", seen)
	}
}
