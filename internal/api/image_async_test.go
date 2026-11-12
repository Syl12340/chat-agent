package api

import (
	"strings"
	"testing"

	"chat-agent/internal/runlog"
)

// The async branch describes the drawing tool by index. When image_health moved to
// a plugin, the slice shrank to one element and this branch indexed past the end —
// every turn with a pending request panicked. Nothing caught it because every test
// called imageTools without a request, so the branch never ran.
func TestImageToolsAsyncBranchStaysInsideTheSlice(t *testing.T) {
	server := stubServer(t)
	registry, err := runlog.OpenRegistry(t.TempDir())
	if err != nil {
		t.Skipf("runtime registry unavailable: %v", err)
	}
	server.runtime = registry

	tools := server.imageTools("role", "session", "pending-request")
	if len(tools) == 0 {
		t.Fatal("the async branch produced no tools")
	}
	// The drawing tool must still be first and still say what it costs.
	if tools[0].Name != "image_generate" {
		t.Fatalf("first host-side image tool should be image_generate, got %q", tools[0].Name)
	}
	if !strings.Contains(tools[0].Usage, "one job per conversation turn") {
		t.Fatalf("async usage guidance was not applied: %q", tools[0].Usage)
	}
}
