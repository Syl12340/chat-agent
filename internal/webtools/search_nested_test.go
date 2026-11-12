package webtools

import "testing"

// A result block whose summary contains a nested list must still yield its own
// link. Matching to the first </li> cut the block before the <h2>, and whatever
// anchor fell inside the truncated range became the "result" — which is how a
// query for one topic came back with unrelated sites.
func TestParseResultsSurvivesNestedList(t *testing.T) {
	page := `<ol id="b_results">
<li class="b_algo"><h2><a href="https://docs.kernel.org/userspace-api/landlock.html">Landlock 用户空间 API</a></h2>
<p>Ruleset 与访问权限说明</p>
<ul><li>子项一</li><li>子项二</li></ul></li>
<li class="b_algo"><h2><a href="https://arxiv.org/abs/2002.09410">Landlock 论文</a></h2>
<p>摘要与作者</p></li>
</ol>`

	results := parseResults(page)
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d: %+v", len(results), results)
	}
	if results[0].URL != "https://docs.kernel.org/userspace-api/landlock.html" || results[0].Title != "Landlock 用户空间 API" {
		t.Fatalf("first result is wrong: %+v", results[0])
	}
	if results[0].Snippet != "Ruleset 与访问权限说明" {
		t.Fatalf("first snippet is wrong: %q", results[0].Snippet)
	}
	if results[1].URL != "https://arxiv.org/abs/2002.09410" {
		t.Fatalf("second result is wrong: %+v", results[1])
	}
}

// Navigation and footer anchors outside any result block must never be reported.
func TestParseResultsIgnoresPageChrome(t *testing.T) {
	page := `<nav><a href="https://unrelated.example/nav">导航</a></nav>
<li class="b_algo"><h2><a href="https://real.example/page">真正的结果</a></h2><p>说明</p></li>
<footer><a href="https://beian.example/footer">备案</a></footer>`

	results := parseResults(page)
	if len(results) != 1 || results[0].URL != "https://real.example/page" {
		t.Fatalf("page chrome leaked into results: %+v", results)
	}
}

// A block with no heading must be skipped rather than borrowing the next block's
// heading, which the previous single-regex form could do after truncation.
func TestParseResultsSkipsBlockWithoutHeading(t *testing.T) {
	page := `<li class="b_algo"><p>只有一个摘要，没有标题</p></li>
<li class="b_algo"><h2><a href="https://real.example/second">第二条</a></h2><p>摘要</p></li>`

	results := parseResults(page)
	if len(results) != 1 || results[0].URL != "https://real.example/second" {
		t.Fatalf("want only the block that has a heading, got %+v", results)
	}
}
