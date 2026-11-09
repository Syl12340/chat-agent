package search

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"pocket-agent/internal/pluginapi"
	"pocket-agent/internal/webtools"
)

// newClient builds a search-only client. The outbound boundary (HTTPS only,
// private ranges refused) belongs to webtools and is tested there; these tests
// cover the plugin seam — manifest, registration and capability filtering.
func newClient(t *testing.T) *webtools.Client {
	t.Helper()
	client, err := webtools.New(webtools.Config{Enabled: true, SearchURL: "https://cn.bing.com/search"})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func loadPlugin(t *testing.T) *pluginapi.Catalog {
	t.Helper()
	loader := pluginapi.NewLoader()
	loader.Add(New(newClient(t)))
	catalog, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestUnconfiguredClientYieldsNoPlugin(t *testing.T) {
	// Nothing configured: the capability should not exist at all, rather than
	// existing and failing at call time.
	if plugin := New(nil); plugin != nil {
		t.Fatal("nil client must not produce a plugin")
	}
	client, err := webtools.New(webtools.Config{})
	if err != nil {
		t.Fatalf("empty config should still build a client: %v", err)
	}
	if plugin := New(client); plugin != nil {
		t.Fatal("client without search or fetch must not produce a plugin")
	}
}

func TestManifestReflectsConfiguredAbilities(t *testing.T) {
	plugin := New(newClient(t))
	if plugin == nil {
		t.Fatal("plugin should exist once search is configured")
	}
	manifest := plugin.Manifest()
	if manifest.Metadata.ID != PluginID {
		t.Fatalf("unexpected id %s", manifest.Metadata.ID)
	}
	if !manifest.Capability(CapabilitySearch) {
		t.Fatal("search capability missing")
	}
	// A search endpoint also enables page reading: the search itself has to fetch
	// and parse engine HTML, so webtools reports both abilities.
	if !manifest.Capability(CapabilityFetch) {
		t.Fatal("fetch capability should accompany a configured search endpoint")
	}
	if !manifest.DeclaresTool("web_search") || !manifest.DeclaresTool("web_fetch") {
		t.Fatalf("tools must match the declared capabilities: %v", manifest.Spec.Tools)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("plugin manifest is invalid: %v", err)
	}
}

// The manifest and the registrations must agree, or the loader refuses the plugin.
func TestRegistersThroughLoader(t *testing.T) {
	catalog := loadPlugin(t)
	if len(catalog.Unavailable()) != 0 {
		t.Fatalf("plugin should load cleanly: %+v", catalog.Unavailable())
	}
	tool, found := catalog.Tool("web_search")
	if !found {
		t.Fatal("web_search not registered")
	}
	if tool.PluginID != PluginID || tool.Tool.Capability != CapabilitySearch {
		t.Fatalf("unexpected tool metadata: %+v", tool)
	}
	if _, found := catalog.Tool("web_fetch"); !found {
		t.Fatal("web_fetch should be registered alongside search")
	}
	// A disabled capability is hidden, but the plugin stays installed.
	if admitted := catalog.Admit(pluginapi.Select("other.capability")); len(admitted.Tools) != 0 {
		t.Fatalf("a disabled capability must not be admitted: %+v", admitted.Tools)
	}
	if admitted := catalog.Admit(pluginapi.Select(CapabilitySearch)); len(admitted.Tools) != 1 {
		t.Fatalf("only the enabled capability may be admitted: %+v", admitted.Tools)
	}
	if admitted := catalog.Admit(pluginapi.AllowAll()); len(admitted.Tools) != 2 {
		t.Fatalf("AllowAll should admit both tools: %+v", admitted.Tools)
	}
	if len(catalog.Tools()) != 2 {
		t.Fatal("filtering the admission must not remove tools from the catalog")
	}
}

func TestSearchToolValidatesArguments(t *testing.T) {
	catalog := loadPlugin(t)
	tool, _ := catalog.Tool("web_search")
	cases := map[string]string{
		"unknown field": `{"query":"x","extra":1}`,
		"missing query": `{}`,
		"blank query":   `{"query":"   "}`,
		"not an object": `"just a string"`,
	}
	for name, args := range cases {
		if _, err := tool.Tool.Handler(context.Background(), json.RawMessage(args)); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}
	// A single character is refused by the shared query repair, not by luck.
	if _, err := tool.Tool.Handler(context.Background(), json.RawMessage(`{"query":"鲸"}`)); err == nil {
		t.Fatal("single-character query must be rejected")
	}
}

func TestHandlerReportsUpstreamFailure(t *testing.T) {
	// A cancelled context must surface as an error rather than an empty result.
	catalog := loadPlugin(t)
	tool, _ := catalog.Tool("web_search")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tool.Tool.Handler(ctx, json.RawMessage(`{"query":"大肥鱼"}`)); err == nil {
		t.Fatal("cancelled context must fail the call")
	}
}

func TestCapabilityNamesAreStable(t *testing.T) {
	// These strings appear in manifests, user config and plugin docs. Changing one
	// silently disables a capability, so pin them.
	if CapabilitySearch != "search.web" || CapabilityFetch != "search.fetch" || PluginID != "builtin.search" {
		t.Fatalf("capability identifiers changed: %s %s %s", CapabilitySearch, CapabilityFetch, PluginID)
	}
	if !strings.Contains(New(newClient(t)).Manifest().Metadata.Name, "Search") {
		t.Fatal("unexpected display name")
	}
}
