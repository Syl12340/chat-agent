package pluginapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// stub is the smallest possible plugin: one tool, no dependencies.
type stub struct {
	manifest Manifest
	tools    []Tool
	started  bool
	stopped  bool
}

func (s *stub) Manifest() Manifest { return s.manifest }

func (s *stub) Register(r *Registry) error {
	for _, tool := range s.tools {
		if err := r.Tool(tool); err != nil {
			return err
		}
	}
	return nil
}

func (s *stub) Start(context.Context, Host) error { s.started = true; return nil }
func (s *stub) Stop(context.Context) error        { s.stopped = true; return nil }

func baseManifest(id string, tools ...string) Manifest {
	return Manifest{
		APIVersion: APIVersion,
		Kind:       Kind,
		Metadata:   Metadata{ID: id, Name: id, Version: "1.0.0"},
		Spec: Spec{
			Runtime:      RuntimeBuiltin,
			Capabilities: []string{"demo.run"},
			Tools:        tools,
		},
	}
}

func okTool(name string) Tool {
	return Tool{
		Name: name, Capability: "demo.run", Order: 1,
		Parameters: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Handler: func(context.Context, json.RawMessage) (Result, error) {
			return Result{Output: "ok"}, nil
		},
	}
}

func TestManifestRejectsUnsupportedDeclarations(t *testing.T) {
	cases := map[string]func(*Manifest){
		"api version":   func(m *Manifest) { m.APIVersion = "chat-agent.dev/v9" },
		"kind":          func(m *Manifest) { m.Kind = "Bundle" },
		"id":            func(m *Manifest) { m.Metadata.ID = "Not Valid" },
		"version":       func(m *Manifest) { m.Metadata.Version = "1.0" },
		"runtime":       func(m *Manifest) { m.Spec.Runtime = "wasm" },
		"no capability": func(m *Manifest) { m.Spec.Capabilities = nil },
		"capability shape": func(m *Manifest) { m.Spec.Capabilities = []string{"search"} },
		"tool name":     func(m *Manifest) { m.Spec.Tools = []string{"Bad Tool"} },
		"absolute path": func(m *Manifest) { m.Spec.Permissions.Filesystem = []string{"/etc/passwd"} },
		"dotdot":        func(m *Manifest) { m.Spec.Permissions.Filesystem = []string{"memory://../x"} },
		"sessions":      func(m *Manifest) { m.Spec.Permissions.Sessions = "write" },
		"self dep": func(m *Manifest) {
			m.Spec.Provides = []string{"demo.svc"}
			m.Spec.Dependencies = []string{"demo.svc"}
		},
		"bad config": func(m *Manifest) { m.Spec.Config = json.RawMessage(`{not json`) },
	}
	for name, mutate := range cases {
		manifest := baseManifest("demo.plugin", "demo_tool")
		mutate(&manifest)
		if err := manifest.Validate(); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}
	if err := baseManifest("demo.plugin", "demo_tool").Validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
}

func TestRuntimeServiceAndSandboxAreDeclaredButRefused(t *testing.T) {
	for _, runtime := range []string{RuntimeService, RuntimeSandbox} {
		manifest := baseManifest("demo.plugin", "demo_tool")
		manifest.Spec.Runtime = runtime
		err := manifest.Validate()
		if err == nil || !strings.Contains(err.Error(), "尚未实现") {
			t.Fatalf("%s: should be refused with a clear message, got %v", runtime, err)
		}
	}
}

func TestRegisterChecksAgainstManifest(t *testing.T) {
	plugin := &stub{manifest: baseManifest("demo.plugin", "declared_tool"), tools: []Tool{okTool("undeclared_tool")}}
	loader := NewLoader()
	loader.Add(plugin)
	if _, err := loader.Load(); err == nil || !strings.Contains(err.Error(), "没有在 manifest") {
		t.Fatalf("undeclared tool must be refused, got %v", err)
	}

	foreign := okTool("declared_tool")
	foreign.Capability = "other.capability"
	plugin = &stub{manifest: baseManifest("demo.plugin", "declared_tool"), tools: []Tool{foreign}}
	loader = NewLoader()
	loader.Add(plugin)
	if _, err := loader.Load(); err == nil || !strings.Contains(err.Error(), "引用了未声明的能力") {
		t.Fatalf("undeclared capability must be refused, got %v", err)
	}
}

// The defining behaviour borrowed from Cordis: a plugin with an unmet dependency
// is skipped, and everything else still loads.
func TestMissingDependencySkipsOnlyThatPlugin(t *testing.T) {
	dependent := baseManifest("demo.dependent", "dependent_tool")
	dependent.Spec.Dependencies = []string{"missing.service"}
	healthy := &stub{manifest: baseManifest("demo.healthy", "healthy_tool"), tools: []Tool{okTool("healthy_tool")}}
	broken := &stub{manifest: dependent, tools: []Tool{okTool("dependent_tool")}}

	loader := NewLoader()
	loader.Add(broken)
	loader.Add(healthy)
	catalog, err := loader.Load()
	if err != nil {
		t.Fatalf("a missing dependency must not fail the load: %v", err)
	}
	if _, found := catalog.Tool("dependent_tool"); found {
		t.Fatal("dependent tool must not be installed")
	}
	if _, found := catalog.Tool("healthy_tool"); !found {
		t.Fatal("the healthy plugin must still load")
	}
	unavailable := catalog.Unavailable()
	if len(unavailable) != 1 || unavailable[0].PluginID != "demo.dependent" {
		t.Fatalf("unavailable must record the plugin and reason: %+v", unavailable)
	}
	if !strings.Contains(unavailable[0].Reason, "missing.service") {
		t.Fatalf("reason should name the service: %s", unavailable[0].Reason)
	}
}

func TestSatisfiedDependencyLoads(t *testing.T) {
	provider := baseManifest("demo.provider")
	provider.Spec.Provides = []string{"demo.service"}
	provider.Spec.Tools = nil
	consumer := baseManifest("demo.consumer", "consumer_tool")
	consumer.Spec.Dependencies = []string{"demo.service"}

	loader := NewLoader()
	loader.Add(&stub{manifest: provider})
	loader.Add(&stub{manifest: consumer, tools: []Tool{okTool("consumer_tool")}})
	catalog, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Unavailable()) != 0 {
		t.Fatalf("nothing should be unavailable: %+v", catalog.Unavailable())
	}
	if _, found := catalog.Tool("consumer_tool"); !found {
		t.Fatal("consumer tool should be installed")
	}
}

func TestDuplicateToolNamesAcrossPluginsAreRejected(t *testing.T) {
	loader := NewLoader()
	loader.Add(&stub{manifest: baseManifest("demo.one", "same_tool"), tools: []Tool{okTool("same_tool")}})
	loader.Add(&stub{manifest: baseManifest("demo.two", "same_tool"), tools: []Tool{okTool("same_tool")}})
	if _, err := loader.Load(); err == nil || !strings.Contains(err.Error(), "同时由") {
		t.Fatalf("duplicate tool names must be refused, got %v", err)
	}
}

func TestAdmitFiltersByCapability(t *testing.T) {
	a := baseManifest("demo.a", "tool_a")
	a.Spec.Capabilities = []string{"alpha.run"}
	b := baseManifest("demo.b", "tool_b")
	b.Spec.Capabilities = []string{"beta.run"}

	loader := NewLoader()
	loader.Add(&stub{manifest: a, tools: []Tool{func() Tool { x := okTool("tool_a"); x.Capability = "alpha.run"; return x }()}})
	loader.Add(&stub{manifest: b, tools: []Tool{func() Tool { x := okTool("tool_b"); x.Capability = "beta.run"; return x }()}})
	catalog, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}

	admitted := catalog.Admit(Select("alpha.run"))
	if len(admitted.Tools) != 1 || admitted.Tools[0].Tool.Name != "tool_a" {
		t.Fatalf("only the enabled capability may be admitted: %+v", admitted.Tools)
	}
	if all := catalog.Admit(AllowAll()); len(all.Tools) != 2 {
		t.Fatalf("AllowAll should admit both: %+v", all.Tools)
	}
	// Installation is not exposure: both stay in the catalog either way.
	if len(catalog.Tools()) != 2 {
		t.Fatal("filtering the admission must not remove tools from the catalog")
	}
}

func TestTypedNilPluginIsIgnored(t *testing.T) {
	var plugin *stub
	loader := NewLoader()
	loader.Add(plugin) // typed nil stored in the interface
	if _, err := loader.Load(); err != nil {
		t.Fatalf("a typed nil plugin must be ignored, not panic: %v", err)
	}
}

func TestStartAllRollsBackOnFailure(t *testing.T) {
	first := &stub{manifest: baseManifest("demo.first", "first_tool"), tools: []Tool{okTool("first_tool")}}
	failing := &failingPlugin{stub: stub{manifest: baseManifest("demo.failing", "failing_tool")}}
	failing.tools = []Tool{okTool("failing_tool")}

	loader := NewLoader()
	loader.Add(first)
	loader.Add(failing)
	catalog, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.StartAll(context.Background(), func(Manifest) Host { return nil }); err == nil {
		t.Fatal("expected start failure")
	}
	if !first.started || !first.stopped {
		t.Fatalf("already-started plugins must be stopped on failure: %+v", first)
	}
}

type failingPlugin struct{ stub }

func (f *failingPlugin) Start(context.Context, Host) error { return errors.New("boom") }

func TestParseRejectsUnknownFields(t *testing.T) {
	doc := `{"apiVersion":"chat-agent.dev/v1","kind":"Plugin","metadata":{"id":"demo.x","name":"x","version":"1.0.0"},"spec":{"runtime":"builtin","capabilities":["demo.run"],"permissions":{},"tools":[]},"extra":1}`
	if _, err := Parse([]byte(doc)); err == nil {
		t.Fatal("unknown top-level fields must be refused")
	}
}
