package webtools

import (
	"strings"
	"testing"
)

const samplePage = `<!doctype html><html><head><title>招生简章 - 示例大学</title></head><body>
<nav><a href="/nav-link">导航项</a></nav>
<script>var ignored = "脚本内容";</script>
<h1>2027 年招生简章</h1>
<p>报名条件见下表，详情参考<a href="/apply/guide">报考指南</a>。</p>
<h2>招生计划</h2>
<table><caption>分专业计划</caption>
<tr><th>专业</th><th>人数</th></tr>
<tr><td>数学</td><td>60</td></tr>
<tr><td>物理</td><td>45</td></tr>
</table>
<p>外链：<a href="https://example.org/x">外部资料</a>，脚本：<a href="javascript:alert(1)">不要</a>，邮件：<a href="mailto:a@b.c">写信</a></p>
</body></html>`

// The reported defect: a page reached the model as one flattened string, so the
// headings, links and table rows that a next step needs were gone.
func TestParsePageKeepsStructure(t *testing.T) {
	page := ParsePage("https://example.edu/admission", []byte(samplePage), false)

	if page.Title != "招生简章 - 示例大学" {
		t.Fatalf("title = %q", page.Title)
	}
	if len(page.Headings) == 0 || page.Headings[0].Level != 1 || !strings.Contains(page.Headings[0].Text, "招生简章") {
		t.Fatalf("headings lost: %+v", page.Headings)
	}
	// Markdown, not prose: a heading must be recognisable as one.
	if !strings.Contains(page.Content, "# 2027 年招生简章") {
		t.Fatalf("heading missing from the rendered text: %q", page.Content)
	}
	// Script content is never reading text.
	if strings.Contains(page.Content, "脚本内容") {
		t.Fatalf("script text leaked into the page: %q", page.Content)
	}
	// A menu link is collected but must not become part of the article prose.
	if strings.Contains(page.Content, "导航项") {
		t.Fatalf("navigation text leaked into the article: %q", page.Content)
	}
}

func TestParsePageRendersTablesAsMarkdown(t *testing.T) {
	page := ParsePage("https://example.edu/admission", []byte(samplePage), false)
	if page.TableCount != 1 {
		t.Fatalf("table count = %d", page.TableCount)
	}
	table := page.Tables[0]
	if table.Caption != "分专业计划" {
		t.Fatalf("caption = %q", table.Caption)
	}
	if len(table.Rows) != 3 || table.Rows[1][0] != "数学" {
		t.Fatalf("rows lost: %+v", table.Rows)
	}
	if !strings.Contains(page.Content, "| 专业 | 人数 |") || !strings.Contains(page.Content, "| 数学 | 60 |") {
		t.Fatalf("table not rendered as markdown: %q", page.Content)
	}
	// The cells must appear once, not twice: rendered as a table and again as prose.
	if strings.Count(page.Content, "数学") != 1 {
		t.Fatalf("table cells duplicated: %q", page.Content)
	}
}

// A relative link is only usable once resolved against the address the document
// came from, and a link that cannot be navigated is not a destination.
func TestParsePageResolvesLinks(t *testing.T) {
	page := ParsePage("https://example.edu/admission", []byte(samplePage), false)
	byText := map[string]string{}
	for _, link := range page.Links {
		byText[link.Text] = link.URL
	}
	if got := byText["报考指南"]; got != "https://example.edu/apply/guide" {
		t.Fatalf("relative link resolved to %q", got)
	}
	if got := byText["外部资料"]; got != "https://example.org/x" {
		t.Fatalf("absolute link resolved to %q", got)
	}
	if got := byText["不要"]; got != "" {
		t.Fatalf("a javascript: target must not become a destination, got %q", got)
	}
	if got := byText["写信"]; got != "" {
		t.Fatalf("a mailto: target must not become a destination, got %q", got)
	}
	// The link references are what a later call acts on, so they must be present
	// and unique.
	seen := map[string]bool{}
	for _, link := range page.Links {
		if link.Ref == "" || seen[link.Ref] {
			t.Fatalf("link reference %q is missing or repeated", link.Ref)
		}
		seen[link.Ref] = true
	}
}

func TestParsePageClassifiesAKnowledgeBaseAsAnIndex(t *testing.T) {
	var rows strings.Builder
	rows.WriteString(`<html><body><h1>课程目录</h1><ul>`)
	for i := 0; i < 20; i++ {
		rows.WriteString(`<li><a href="/lesson/` + string(rune('a'+i)) + `">第 ` + string(rune('a'+i)) + ` 课</a></li>`)
	}
	rows.WriteString(`</ul></body></html>`)
	page := ParsePage("https://example.edu/courses", []byte(rows.String()), false)
	if page.Kind != KindIndex {
		t.Fatalf("kind = %q, want index for a link-heavy page with little prose", page.Kind)
	}
}

func TestParsePageReportsAnEmptyShell(t *testing.T) {
	page := ParsePage("https://example.edu/app", []byte(`<html><body><div id="root"></div><script src="/app.js"></script></body></html>`), false)
	if page.Kind != KindEmpty {
		t.Fatalf("kind = %q, want empty for a page whose text is only built by scripts", page.Kind)
	}
	if page.Parse != "empty" {
		t.Fatalf("parse quality = %q", page.Parse)
	}
}

// A verification page has text, so returning it as the article would be a
// confident lie.
func TestChallengePageIsRecognised(t *testing.T) {
	for _, text := range []string{
		"Please complete the captcha to continue",
		"Checking your browser before accessing",
		"请完成安全验证后继续",
	} {
		if !challengePage("", text) {
			t.Fatalf("%q should be recognised as a verification page", text)
		}
	}
	if challengePage("招生简章", "这是正常的招生正文，没有任何验证。") {
		t.Fatal("ordinary text must not be mistaken for a challenge")
	}
}
