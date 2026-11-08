package channel

import (
	"strings"
	"testing"
)

func TestRenderTextHidesChatUIDeclaration(t *testing.T) {
	input := "先看这个：\n\n```chat-ui\n{\"schema_version\":1,\"id\":\"x\"}\n```\n\n最后一句。"
	got := RenderText(input)
	if strings.Contains(got, "schema_version") || !strings.Contains(got, "先看这个") || !strings.Contains(got, "最后一句") || !strings.Contains(got, "网页交互组件") {
		t.Fatalf("rendered text = %q", got)
	}
}
