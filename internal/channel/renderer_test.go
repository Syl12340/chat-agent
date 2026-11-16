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

func TestRenderBubbles(t *testing.T) {
	got := RenderBubbles("我在。\n\n慢慢说。\n\n不用急。")
	if len(got) != 3 || got[1] != "慢慢说。" {
		t.Fatalf("short conversational reply was not split: %#v", got)
	}
	if got = RenderBubbles("- 第一项\n\n- 第二项"); len(got) != 1 {
		t.Fatalf("structured reply was split: %#v", got)
	}
}
