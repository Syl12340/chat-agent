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
