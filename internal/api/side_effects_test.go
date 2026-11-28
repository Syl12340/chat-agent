package api

import (
	"testing"

	"chat-agent/internal/turns"
)

// A reply claiming the work is done carries no weight when the operations behind
// it failed. This is the count that turns such a reply into a caveat instead of
// a clean success.
func TestFailedSideEffectsCountsOnlyFailedMutations(t *testing.T) {
	events := []turns.ToolEvent{
		{Name: "workspace_write", Error: "disk full"},
		{Name: "workspace_write"},
		{Name: "memory_search", Error: "index unavailable"},
		{Name: "workspace_shell", Error: "   "},
		{Name: "image_generate", Error: "timed out"},
		{Name: "web_search", Error: "network down"},
	}
	if got := failedSideEffects(events); got != 2 {
		t.Fatalf("failedSideEffects = %d, want 2 (write + image, read-only ignored)", got)
	}
}

func TestFailedSideEffectsIgnoresSuccessfulTurns(t *testing.T) {
	events := []turns.ToolEvent{
		{Name: "workspace_write"},
		{Name: "reminder_add"},
		{Name: "web_fetch"},
	}
	if got := failedSideEffects(events); got != 0 {
		t.Fatalf("a turn with no failed mutations must not be flagged, got %d", got)
	}
	if got := failedSideEffects(nil); got != 0 {
		t.Fatalf("empty events = %d, want 0", got)
	}
}
