package channel

import (
	"strings"
	"unicode/utf8"
)

const componentFallback = "[此回复包含网页交互组件，请在 chat-agent 网页中查看。]"

// RenderText is a deterministic degradation from the Web/PWA response to a
// plain messaging channel. It never exposes chat-ui declarations themselves.
func RenderText(input string) string {
	lines := strings.Split(input, "\n")
	out := make([]string, 0, len(lines))
	inComponent := false
	hadComponent := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inComponent && strings.HasPrefix(trimmed, "```chat-ui") {
			inComponent = true
			hadComponent = true
			continue
		}
		if inComponent {
			if strings.HasPrefix(trimmed, "```") {
				inComponent = false
			}
			continue
		}
		out = append(out, line)
	}
	text := strings.TrimSpace(strings.Join(out, "\n"))
	if hadComponent {
		if text != "" {
			text += "\n\n"
		}
		text += componentFallback
	}
	if len(text) > 60<<10 {
		cut := 60 << 10
		for cut > 0 && !utf8.ValidString(text[:cut]) {
			cut--
		}
		text = text[:cut] + "\n\n[回复过长，完整内容请在 chat-agent 网页中查看。]"
	}
	return text
}

// RenderBubbles projects a short conversational reply into at most three IM
// messages. It mirrors the web projection: explicit legacy separators and
// short blank-line paragraphs may split, while Markdown structures remain one
// atomic message. Sending remains one delivery intent, so a partial platform
// send is settled as unknown rather than retried piecemeal.
func RenderBubbles(input string) []string {
	text := RenderText(input)
	if text == "" {
		return nil
	}
	explicit := strings.Contains(text, "|||")
	visible := strings.ReplaceAll(text, "|||", "\n\n")
	if utf8.RuneCountInString(text) > 1200 || structuredReply(visible) {
		return []string{strings.TrimSpace(visible)}
	}
	separator := "\n\n"
	if explicit {
		separator = "|||"
	}
	raw := strings.Split(text, separator)
	parts := make([]string, 0, len(raw))
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item != "" {
			parts = append(parts, item)
		}
	}
	if len(parts) < 2 || len(parts) > 3 {
		return []string{strings.TrimSpace(visible)}
	}
	for _, part := range parts {
		if utf8.RuneCountInString(part) > 600 {
			return []string{strings.TrimSpace(visible)}
		}
	}
	return parts
}

func structuredReply(text string) bool {
	if strings.ContainsAny(text, "`$\\[]<>|~") {
		return true
	}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "    ") || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "+ ") {
			return true
		}
		if len(trimmed) >= 2 && trimmed[0] >= '0' && trimmed[0] <= '9' && (trimmed[1] == '.' || trimmed[1] == ')') {
			return true
		}
	}
	return false
}
