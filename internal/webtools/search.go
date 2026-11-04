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
	resultBlock = regexp.MustCompile(`(?is)<li[^>]*class="[^"]*\bb_algo\b[^"]*"[^>]*>(.*?)</li>`)
	resultLink  = regexp.MustCompile(`(?is)<h2[^>]*>\s*<a[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	resultPara  = regexp.MustCompile(`(?is)<p[^>]*>(.*?)</p>`)
	anyTag      = regexp.MustCompile(`(?s)<[^>]*>`)
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
func parseResults(page string) []Result {
	out := []Result{}
	seen := map[string]bool{}
	for _, block := range resultBlock.FindAllStringSubmatch(page, 20) {
		link := resultLink.FindStringSubmatch(block[1])
		if link == nil {
			continue
		}
		target := unwrapEngineLink(link[1])
		parsed, err := parseURL(target)
		if err != nil {
			continue
		}
		link_URL := parsed.String()
		if seen[link_URL] {
			continue
		}
		title := text(link[2])
		if title == "" {
			continue
		}
		snippet := ""
		if para := resultPara.FindStringSubmatch(block[1]); para != nil {
			snippet = text(para[1])
		}
		seen[link_URL] = true
		out = append(out, Result{Title: bounded(title, 512), URL: link_URL, Snippet: bounded(snippet, 2048)})
	}
	return out
}
