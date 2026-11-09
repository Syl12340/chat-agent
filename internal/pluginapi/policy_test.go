package pluginapi

import (
	"context"
	"encoding/json"
	"testing"
)

// capTool is okTool with a chosen capability, for tests that need two plugins to
// carry distinct capabilities.
func capTool(name, capability string) Tool {
	return Tool{
		Name: name, Capability: capability, Order: 1,
		Parameters: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Handler: func(context.Context, json.RawMessage) (Result, error) {
			return Result{Output: "ok"}, nil
		},
	}
}

func TestPolicyParsesGrants(t *testing.T) {
	policy, err := NewPolicy(
		[]string{"demo.plugin"},
		[]string{"demo.plugin:filesystem:memory://role", "other.plugin:network:public-https"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if policy.PluginEnabled("demo.plugin") {
		t.Fatal("a disabled plugin must not report as enabled")
	}
	if !policy.PluginEnabled("other.plugin") {
		t.Fatal("a plugin left off the disabled list keeps loading")
	}
	// The first colon separates id from permission, so the namespace keeps its own.
	if !policy.Grant("demo.plugin", "filesystem:memory://role") {
		t.Fatal("grant with a namespaced permission was not recorded")
	}
	if policy.Grant("demo.plugin", "filesystem:/etc/passwd") {
		t.Fatal("an ungranted namespace must not be allowed")
	}
	if policy.Grant("other.plugin", "filesystem:memory://role") {
		t.Fatal("a grant belongs to one plugin only")
	}
}

func TestPolicyRejectsMalformedEntries(t *testing.T) {
	for _, grants := range [][]string{{"no-colon"}, {"demo.plugin:"}, {":network:public-https"}, {"Demo.Plugin:x"}} {
		if _, err := NewPolicy(nil, grants); err == nil {
			t.Fatalf("grants %q: expected rejection", grants)
		}
	}
	for _, disabled := range [][]string{{"nodots"}, {"Demo.Plugin"}} {
		if _, err := NewPolicy(disabled, nil); err == nil {
			t.Fatalf("disabled %q: expected rejection", disabled)
		}
	}
}

func TestZeroPolicyLoadsEverythingAndGrantsNothing(t *testing.T) {
	var policy Policy
	loader := NewLoader()
	loader.SetPolicy(policy)
	loader.Add(&stub{manifest: baseManifest("demo.plugin", "demo_tool"), tools: []Tool{okTool("demo_tool")}})
	catalog, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Tools()) != 1 {
		t.Fatalf("an unconfigured host still runs what the build ships: %d tools", len(catalog.Tools()))
	}
	if policy.Grant("demo.plugin", "filesystem:memory://role") {
		t.Fatal("an unconfigured host grants no permission")
	}
}

func TestDisabledPluginDoesNotLoad(t *testing.T) {
	policy, err := NewPolicy([]string{"demo.plugin"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	loader := NewLoader()
	loader.SetPolicy(policy)
	loader.Add(&stub{manifest: baseManifest("demo.plugin", "demo_tool"), tools: []Tool{okTool("demo_tool")}})
	catalog, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Tools()) != 0 {
		t.Fatal("a disabled plugin must not expose tools")
	}
	unavailable := catalog.Unavailable()
	if len(unavailable) != 1 || unavailable[0].PluginID != "demo.plugin" {
		t.Fatalf("the off switch must be visible as unavailable: %+v", unavailable)
	}
	if unavailable[0].Reason != "宿主已停用" {
		t.Fatalf("reason should name the host's decision: %q", unavailable[0].Reason)
	}
}

// A disabled provider cannot satisfy anybody's dependency: the consumer would
// otherwise start against a service that never loads.
func TestDisabledProviderDoesNotSatisfyDependencies(t *testing.T) {
	provider := baseManifest("demo.provider", "provider_tool")
	provider.Spec.Provides = []string{"demo.svc"}
	consumer := baseManifest("demo.consumer", "consumer_tool")
	consumer.Spec.Dependencies = []string{"demo.svc"}
	policy, err := NewPolicy([]string{"demo.provider"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	loader := NewLoader()
	loader.SetPolicy(policy)
	loader.Add(&stub{manifest: provider, tools: []Tool{okTool("provider_tool")}})
	loader.Add(&stub{manifest: consumer, tools: []Tool{okTool("consumer_tool")}})
	catalog, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Tools()) != 0 {
		t.Fatal("neither a disabled provider nor its unstartable consumer may expose tools")
	}
	var consumerReason string
	for _, item := range catalog.Unavailable() {
		if item.PluginID == "demo.consumer" {
			consumerReason = item.Reason
		}
	}
	if consumerReason == "" {
		t.Fatalf("the consumer must stay unavailable: %+v", catalog.Unavailable())
	}
}

func TestEnablementDropsCapabilitiesOfStoppedPlugins(t *testing.T) {
	first := baseManifest("demo.a", "tool_a")
	first.Spec.Capabilities = []string{"cap.a"}
	second := baseManifest("demo.b", "tool_b")
	second.Spec.Capabilities = []string{"cap.b"}
	policy, err := NewPolicy([]string{"demo.b"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	loader := NewLoader()
	loader.SetPolicy(policy)
	loader.Add(&stub{manifest: first, tools: []Tool{capTool("tool_a", "cap.a")}})
	loader.Add(&stub{manifest: second, tools: []Tool{capTool("tool_b", "cap.b")}})
	catalog, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	enable := policy.Enablement(catalog)
	if !enable.Enabled("cap.a") {
		t.Fatal("an enabled plugin keeps its capability")
	}
	if enable.Enabled("cap.b") {
		t.Fatal("a stopped plugin contributes no capability")
	}
	admitted := catalog.Admit(enable)
	if len(admitted.Tools) != 1 || admitted.Tools[0].Tool.Name != "tool_a" {
		t.Fatalf("admission should follow the policy: %+v", admitted.Tools)
	}
}

func TestDeclaredPermissionsListsEveryKind(t *testing.T) {	manifest := baseManifest("demo.plugin", "demo_tool")
	manifest.Spec.Permissions = Permissions{
		Filesystem: []string{"memory://role"},
		Network:    []string{"public-https"},
		Model:      []string{"utility"},
		Sessions:   "read",
	}
	got := DeclaredPermissions(manifest)
	want := []string{"filesystem:memory://role", "model:utility", "network:public-https", "sessions:read"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}
