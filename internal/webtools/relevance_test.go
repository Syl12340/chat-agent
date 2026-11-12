package webtools

import "testing"

func TestAssessRelevanceCountsMatches(t *testing.T) {
	results := []Result{
		{Title: "Landlock: Unprivileged Sandboxing", URL: "https://landlock.io/", Snippet: "restricting ambient rights"},
		{Title: "LandLock — Stop Title Theft", URL: "https://www.landlock.com/", Snippet: "property title"},
		{Title: "搜狗输入法", URL: "https://pinyin.sogou.com/", Snippet: "皮肤制作"},
	}
	relevance := AssessRelevance("Landlock unprivileged access control", results)
	if relevance.Total != 3 || relevance.Matched != 2 {
		t.Fatalf("want 2 of 3 matched, got %+v", relevance)
	}
	if relevance.Warning != "" {
		t.Fatalf("a page with matches must not warn: %q", relevance.Warning)
	}
}

// The exact failure this exists for: a query answered entirely with unrelated
// pages. The caller has to be told, or it will present the noise as a finding.
func TestAssessRelevanceWarnsOnUnrelatedResults(t *testing.T) {
	results := []Result{
		{Title: "怎么评价某拆机翻车", URL: "https://www.zhihu.com/question/1", Snippet: "多贴了一层手机膜"},
		{Title: "yandex 的正确用途", URL: "https://www.zhihu.com/question/2", Snippet: "俄罗斯门户"},
	}
	relevance := AssessRelevance("Landlock unprivileged access control Linux", results)
	if relevance.Matched != 0 {
		t.Fatalf("nothing should match, got %+v", relevance)
	}
	if relevance.Warning == "" {
		t.Fatal("unrelated results must carry a warning")
	}
}

// A URL alone counts: a result can be on topic while its snippet is boilerplate.
func TestAssessRelevanceReadsURL(t *testing.T) {
	results := []Result{{Title: "文档", URL: "https://docs.kernel.org/userspace-api/landlock.html", Snippet: ""}}
	if relevance := AssessRelevance("landlock", results); relevance.Matched != 1 {
		t.Fatalf("a matching URL should count, got %+v", relevance)
	}
}

// Chinese has no word breaks, so a query is probed with two-character grams;
// single characters would match nearly any Chinese page.
func TestAssessRelevanceUsesCJKGrams(t *testing.T) {
	onTopic := []Result{{Title: "华东师范大学教务系统入口", URL: "https://example.edu/", Snippet: "选课与成绩"}}
	if relevance := AssessRelevance("华东师范大学 教务系统", onTopic); relevance.Matched != 1 {
		t.Fatalf("a Chinese result containing the query should match, got %+v", relevance)
	}
	offTopic := []Result{{Title: "今日天气", URL: "https://weather.example/", Snippet: "多云转晴"}}
	if relevance := AssessRelevance("华东师范大学", offTopic); relevance.Matched != 0 || relevance.Warning == "" {
		t.Fatalf("unrelated Chinese results must warn, got %+v", relevance)
	}
}

// An empty or punctuation-only query cannot be judged, so it must not accuse the
// engine of returning nonsense.
func TestAssessRelevanceSkipsEmptyQuery(t *testing.T) {
	results := []Result{{Title: "anything", URL: "https://example.org/"}}
	if relevance := AssessRelevance("!!!", results); relevance.Warning != "" {
		t.Fatalf("a query with no terms must not warn: %+v", relevance)
	}
}
