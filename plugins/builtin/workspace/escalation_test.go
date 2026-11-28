package workspace

import (
	"strings"
	"testing"
)

// Choosing a level is ordinary work. Only reaching past it asks the person, which
// is what keeps a confirmation prompt meaningful instead of routine.
func TestShellApprovalOnlyAsksForWidening(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"plain read-only", `{"mode":"read-only","command":"ls"}`, false},
		{"plain workspace-write", `{"mode":"workspace-write","command":"make"}`, false},
		{"repeated level", `{"mode":"workspace-write","command":"make","sandbox_permissions":"workspace-write","justification":"same"}`, false},
		{"widen from read-only", `{"mode":"read-only","command":"make","sandbox_permissions":"workspace-write","justification":"build needs output"}`, true},
		{"widen to unrestricted", `{"mode":"read-only","command":"curl x","sandbox_permissions":"danger-full-access","justification":"needs network"}`, true},
		{"malformed arguments still ask", `not json`, false},
	}
	for _, test := range cases {
		got := shellApprovalReason([]byte(test.raw)) != ""
		if got != test.want {
			t.Errorf("%s: asked=%v, want %v", test.name, got, test.want)
		}
	}
	// The reason is what the person reads, so it must name both levels rather than
	// say "more permissions requested".
	reason := shellApprovalReason([]byte(`{"mode":"read-only","command":"x","sandbox_permissions":"workspace-write","justification":"build needs output"}`))
	if reason == "" {
		t.Fatal("a widening must produce a reason")
	}
	for _, want := range []string{"read-only", "workspace-write", "build needs output"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("reason %q should mention %q", reason, want)
		}
	}
}

// The ladder is one-way: escalation can never be used to narrow, and a target the
// caller could have picked directly must not need approval.
func TestResolveShellModeEnforcesTheLadder(t *testing.T) {
	cases := []struct {
		name, mode, permissions, justification string
		want                                   string
		wantErr                                bool
	}{
		{name: "no escalation", mode: "read-only", want: "read-only"},
		{name: "workspace-write stays itself", mode: "workspace-write", want: "workspace-write"},
		{name: "one step up", mode: "read-only", permissions: "workspace-write", justification: "needs to write", want: "workspace-write"},
		{name: "to the top", mode: "read-only", permissions: "danger-full-access", justification: "needs network", want: "danger-full-access"},
		{name: "from the middle", mode: "workspace-write", permissions: "danger-full-access", justification: "needs network", want: "danger-full-access"},
		{name: "repeating the level is not escalation", mode: "workspace-write", permissions: "workspace-write", justification: "same", wantErr: true},
		{name: "narrowing is refused", mode: "workspace-write", permissions: "read-only", justification: "less", wantErr: true},
		{name: "skipping from the floor", mode: "read-only", permissions: "danger-full-access", justification: "ok", want: "danger-full-access"},
		{name: "reason without a target", mode: "read-only", justification: "orphan", wantErr: true},
		{name: "target without a reason", mode: "read-only", permissions: "workspace-write", wantErr: true},
		{name: "blank reason", mode: "read-only", permissions: "workspace-write", justification: "   ", wantErr: true},
		{name: "unknown mode", mode: "root", wantErr: true},
	}
	for _, test := range cases {
		got, err := resolveShellMode(test.mode, test.permissions, test.justification)
		if test.wantErr {
			if err == nil {
				t.Errorf("%s: expected a refusal, got mode %q", test.name, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error %v", test.name, err)
			continue
		}
		if got != test.want {
			t.Errorf("%s: mode = %q, want %q", test.name, got, test.want)
		}
	}
}

// The result has to report the level that actually applied, because a caller
// reading "sockets denied" must be able to trust it.
func TestEscalatedLevelIsWhatGetsReported(t *testing.T) {
	effective, err := resolveShellMode("read-only", "danger-full-access", "needs network")
	if err != nil {
		t.Fatal(err)
	}
	if effective != "danger-full-access" {
		t.Fatalf("effective mode = %q", effective)
	}
	if effective == "read-only" {
		t.Fatal("the reported level must be the escalated one, not the requested one")
	}
}
