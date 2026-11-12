package webtools

import (
	"encoding/base64"
	"html"
	"net/url"
	"regexp"
	"strings"
)

// A browser-like UA is required because search engines reject unknown clients
// while still serving the same public results page to any visitor.
const browserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

var (
	// The block start is matched on its own, and a block is then taken to run until
	// the next block starts. Matching `<li ...>(.*?)</li>` instead ends at the first
	// `</li>`, and a result whose summary contains a nested list ends there too — the
	// block is cut before its <h2>, the link is not found, and (worse) whatever
	// anchor happens to fall inside the truncated range becomes the "result".
	resultBlockStart = regexp.MustCompile(`(?is)<li[^>]*class="[^"]*\bb_algo\b[^"]*"[^>]*>`)
	resultLink       = regexp.MustCompile(`(?is)<h2[^>]*>\s*<a[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	resultPara       = regexp.MustCompile(`(?is)<p[^>]*>(.*?)</p>`)
	anyTag           = regexp.MustCompile(`(?s)<[^>]*>`)
	// Engines highlight matched words with inline emphasis; those must not
	// become spaces or a title reads "蓝色 大肥鱼 形象 大 合集".
	inlineTag = regexp.MustCompile(`(?i)</?(?:strong|b|em|i|mark)\s*>`)
)

// text flattens one HTML fragment into readable text.
func text(fragment string) string {
	fragment = inlineTag.ReplaceAllString(fragment, "")
	return strings.Join(strings.Fields(html.UnescapeString(anyTag.ReplaceAllString(fragment, " "))), " ")
}

// unwrapEngineLink resolves the engine's own redirect wrapper. Bing returns
// https://www.bing.com/ck/a?...&u=a1<base64url> where the payload is the target.
func unwrapEngineLink(raw string) string {
	decoded := html.UnescapeString(raw)
	u, err := url.Parse(decoded)
	if err != nil {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(u.Hostname()), "bing.com") && strings.HasPrefix(u.Path, "/ck/a") {
		payload := u.Query().Get("u")
		payload = strings.TrimPrefix(payload, "a1")
		if payload == "" {
			return ""
		}
		if pad := len(payload) % 4; pad != 0 {
			payload += strings.Repeat("=", 4-pad)
		}
		blob, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(payload, "="))
		if err != nil {
			return ""
		}
		return string(blob)
	}
	return decoded
}

// parseResults extracts ordered candidates from an engine results page.
//
// Blocks are delimited by where the next one starts rather than by a closing tag,
// because a result's own markup may contain nested lists. The link and the snippet
// are then read from the head of the block, which is where the engine puts them.
func parseResults(page string) []Result {
	out := []Result{}
	seen := map[string]bool{}
	starts := resultBlockStart.FindAllStringIndex(page, 20)
	for index, start := range starts {
		end := len(page)
		if index+1 < len(starts) {
			end = starts[index+1][0]
		}
		block := page[start[1]:end]
		link := resultLink.FindStringSubmatch(block)
		if link == nil {
			continue
		}
		target := unwrapEngineLink(link[1])
		parsed, err := parseURL(target)
		if err != nil {
			continue
		}
		linkURL := parsed.String()
		if seen[linkURL] {
			continue
		}
		title := text(link[2])
		if title == "" {
			continue
		}
		snippet := ""
		if para := resultPara.FindStringSubmatch(block); para != nil {
			snippet = text(para[1])
		}
		seen[linkURL] = true
		out = append(out, Result{Title: bounded(title, 512), URL: linkURL, Snippet: bounded(snippet, 2048)})
	}
	return out
}
