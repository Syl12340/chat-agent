package probe

import "testing"

// The client needs to know a message was self-initiated; the model must not. Telling
// the model changes how it reads its own history, so provenance is stripped alongside
// the message ID.
func TestModelVisibleMessagesDropsOrigin(t *testing.T) {
	messages := []Message{
		{Role: "assistant", Content: "那份文档可以定一句开头", Origin: "proactive"},
		{Role: "user", Content: "帮我看看"},
	}
	out := modelVisibleMessages(messages)
	if out[0].Origin != "" {
		t.Fatalf("origin must not reach the model: %q", out[0].Origin)
	}
	if out[1].Origin != "" {
		t.Fatalf("ordinary messages must stay unmarked: %q", out[1].Origin)
	}
	if messages[0].Origin != "proactive" {
		t.Fatal("the input slice was mutated")
	}
}
