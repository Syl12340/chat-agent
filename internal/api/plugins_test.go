package api

import (
	"context"
	"encoding/json"
	"testing"

	"chat-agent/internal/pluginapi"
)

// fakePlugin is the smallest plugin the api package can load, so the status and
// host-grant tests do not need a configured web client.
type fakePlugin struct {
	manifest pluginapi.Manifest
	tools    []pluginapi.Tool
}

func (p fakePlugin) Manifest() pluginapi.Manifest { return p.manifest }

func (p fakePlugin) Register(r *pluginapi.Registry) error {
	for _, tool := range p.tools {
		if err := r.Tool(tool); err != nil {
			return err
		}
	}
	return nil
}

func (p fakePlugin) Start(context.Context, pluginapi.Host) error { return nil }
func (p fakePlugin) Stop(context.Context) error                  { return nil }

func demoManifest(id string) pluginapi.Manifest {
	return pluginapi.Manifest{
		APIVersion: pluginapi.APIVersion,
		Kind:       pluginapi.Kind,
		Metadata:   pluginapi.Metadata{ID: id, Name: id, Version: "1.0.0"},
		Spec: pluginapi.Spec{
			Runtime:      pluginapi.RuntimeBuiltin,
			Capabilities: []string{"demo.run"},
			Tools:        []string{"demo_tool"},
			Permissions:  pluginapi.Permissions{Filesystem: []string{"memory://role"}},
		},
	}
}

func demoPlugin() fakePlugin {
	return fakePlugin{
		manifest: demoManifest("demo.plugin"),
		tools: []pluginapi.Tool{{
			Name: "demo_tool", Capability: "demo.run",
			Parameters: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			Handler: func(context.Context, json.RawMessage) (pluginapi.Result, error) {
				return pluginapi.Result{Output: "ok"}, nil
			},
		}},
	}
}

// Storage exists only when the host granted the namespace the manifest asked for.
func TestPluginHostDoesNotTurnDeclarationsIntoPermissions(t *testing.T) {
	manifest := demoManifest("demo.plugin")
	host := newPluginHost(manifest, t.TempDir(), pluginapi.Policy{})
	if host.Allowed("filesystem:memory://role") {
		t.Fatal("a declared filesystem permission must not be granted by default")
	}
	if host.DataDir() != "" {
		t.Fatalf("no storage directory without a grant, got %q", host.DataDir())
	}

	policy, err := pluginapi.NewPolicy(nil, []string{"demo.plugin:filesystem:memory://role"})
	if err != nil {
		t.Fatal(err)
	}
	granted := newPluginHost(manifest, t.TempDir(), policy)
	if !granted.Allowed("filesystem:memory://role") {
		t.Fatal("an explicitly granted namespace must be allowed")
	}
	if granted.DataDir() == "" {
		t.Fatal("a granted storage namespace needs a directory")
	}
}

func TestPluginViewsExplainGrantsAndSwitches(t *testing.T) {
	policy, err := pluginapi.NewPolicy([]string{"demo.off"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	loader := pluginapi.NewLoader()
	loader.SetPolicy(policy)
	loader.Add(demoPlugin())
	off := demoPlugin()
	off.manifest = demoManifest("demo.off")
	off.tools[0].Name = "off_tool"
	off.manifest.Spec.Tools = []string{"off_tool"}
	loader.Add(off)
	catalog, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{plugins: catalog, pluginPolicy: policy}

	views := server.pluginViews()
	if len(views) != 2 {
		t.Fatalf("both installed plugins should be reported: %+v", views)
	}
	if views[0].ID != "demo.off" || views[0].Loaded || views[0].Reason != "宿主已停用" {
		t.Fatalf("a switched-off plugin must show as unavailable: %+v", views[0])
	}
	if views[1].ID != "demo.plugin" || !views[1].Loaded {
		t.Fatalf("the loaded plugin should report its tools: %+v", views[1])
	}
	if len(views[1].Tools) != 1 || views[1].Tools[0] != "demo_tool" {
		t.Fatalf("tools should travel with the plugin view: %+v", views[1].Tools)
	}
	if len(views[1].Permissions) != 1 || views[1].Permissions[0].Granted {
		t.Fatalf("a declared permission must read as not granted: %+v", views[1].Permissions)
	}
}
