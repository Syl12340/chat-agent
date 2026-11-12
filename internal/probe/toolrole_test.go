package probe

import (
	"context"
	"testing"
)

// Memory, skills and preferences are role-scoped, so a plugin that offers any of them
// needs the role at call time. It travels in the tool context next to the session id.
func TestToolRoleComesFromContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), toolRoleKey{}, "companion")
	if got := ToolRole(ctx); got != "companion" {
		t.Fatalf("want companion, got %q", got)
	}
	if got := ToolRole(context.Background()); got != "" {
		t.Fatalf("a context without a role must yield empty, got %q", got)
	}
	//nolint:staticcheck // a nil context is a real caller mistake worth not panicking on
	if got := ToolRole(nil); got != "" {
		t.Fatalf("nil context must yield empty, got %q", got)
	}
}
