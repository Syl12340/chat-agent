package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"chat-agent/internal/pluginapi"
	"chat-agent/internal/probe"
)

// A conversation gains machine visibility at runtime and loses it the same way,
// with no restart in between. Before this, the only way in was editing
// CHAT_HOST_TOOL_SESSIONS and restarting, which cannot answer "let this
// conversation look at the server" at the moment the user asks.
func TestHostToolsAreGrantedPerSessionAtRuntime(t *testing.T) {
	const token = "host-tools-fixture-token-over-32-characters"
	// The workspace plugin stands in for the Linux service plugin: it exposes host
	// tools from the same catalog and is built on every platform, so this test
	// covers the grant plumbing rather than one particular host.
	policy, err := pluginapi.NewPolicy(nil, []string{"builtin.workspace:filesystem:workspace://server"})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	agent := probe.DefaultConfig()
	// No model is called here; the URL only has to be a valid endpoint so the
	// server assembles normally.
	agent.BaseURL, agent.Model, agent.APIKey = "https://example.invalid/v1", "fixture", "fixture-only"
	cfg := Config{
		Agent:       agent,
		AccessToken: token,
		DataDir:     t.TempDir(),
		HostTools:   HostToolsConfig{WorkspaceRoot: root},
		Plugins:     policy,
	}
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app.Handler())
	t.Cleanup(func() { srv.Close(); app.Close(context.Background()) })
	request := func(method, path string, body any, target any, want int) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			encoded, _ := json.Marshal(body)
			reader = strings.NewReader(string(encoded))
		}
		r, _ := http.NewRequest(method, srv.URL+path, reader)
		r.Header.Set("Authorization", "Bearer "+token)
		res, err := srv.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		payload, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, payload)
		}
		if target != nil {
			if err := json.Unmarshal(payload, target); err != nil {
				t.Fatal(err)
			}
		}
	}
	var state struct {
		Configured bool     `json:"configured"`
		Tools      []string `json:"tools"`
		Sessions   []string `json:"sessions"`
	}
	var session sessionInfo
	request("POST", "/api/sessions", map[string]string{}, &session, 201)
	granted := func() bool {
		return slices.Contains(hostToolNamesFor(app, session.ID), "workspace_list")
	}

	// The capability exists and is configured, but this conversation may not see it.
	request("GET", "/api/host-tools", nil, &state, 200)
	if !state.Configured || !slices.Contains(state.Tools, "workspace_list") {
		t.Fatalf("deployment should offer host tools: %+v", state)
	}
	if granted() {
		t.Fatal("a fresh conversation already had machine access")
	}
	request("POST", "/api/host-tools/sessions", map[string]any{"session": session.ID, "allow": true}, &state, 200)
	if !slices.Contains(state.Sessions, session.ID) || !granted() {
		t.Fatalf("grant did not take effect: %+v", state)
	}
	// Persisted before the response, so a restart cannot silently revoke it.
	reopened, err := openHostGrants(filepath.Join(cfg.DataDir, "host-tools.json"))
	if err != nil || !reopened.Allows(session.ID) {
		t.Fatalf("grant was not durable: %v", err)
	}
	request("POST", "/api/host-tools/sessions", map[string]any{"session": session.ID, "allow": false}, &state, 200)
	if slices.Contains(state.Sessions, session.ID) || granted() {
		t.Fatalf("revoke did not take effect: %+v", state)
	}
	// Unknown sessions are refused rather than stored: a grant for a session that
	// does not exist would come alive again if the id were ever reused.
	request("POST", "/api/host-tools/sessions", map[string]any{"session": strings.Repeat("a", 32), "allow": true}, nil, 404)
	request("POST", "/api/host-tools/sessions", map[string]any{"session": session.ID}, nil, 400)
}

func hostToolNamesFor(app *Server, session string) []string {
	names := []string{}
	for _, tool := range app.pluginTools(app.toolCapabilities(), session, "companion") {
		names = append(names, tool.Name)
	}
	return names
}
