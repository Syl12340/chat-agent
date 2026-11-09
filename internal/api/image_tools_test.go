package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pocket-agent/internal/imagegen"
	"pocket-agent/internal/media"
	"pocket-agent/internal/probe"
)

// stubImageClient parks a real, configured client whose runner never runs;
// registration and schema shape are what these tests cover.
func stubImageClient(t *testing.T) *imagegen.Client {
	t.Helper()
	dir := t.TempDir()
	python := filepath.Join(dir, "python.exe")
	script := filepath.Join(dir, "imgctl.py")
	for _, path := range []string{python, script} {
		if err := os.WriteFile(path, []byte("stub"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	client := imagegen.New(imagegen.Config{
		Python: python, Script: script,
		Profiles: []string{"wai_v17", "z_anime_d8"}, DefaultProfile: "wai_v17",
	})
	if client == nil {
		t.Fatal("stub client should be enabled")
	}
	return client
}

// stubServer pairs the image client with a real media store so the tool is
// actually offered; the store lives in the test's temp dir.
func stubServer(t *testing.T) *Server {
	t.Helper()
	store, err := media.Open(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &Server{imagegen: stubImageClient(t), images: store}
}


// imageTool picks a tool by name: the group now holds image_health and
// image_generate, and their order is not part of the contract.
func imageTool(t *testing.T, tools []probe.Tool, name string) probe.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %s not registered; have %d tools", name, len(tools))
	return probe.Tool{}
}

func TestImageToolSchemaRegisters(t *testing.T) {
	server := stubServer(t)
	tools := server.imageTools("role", "session")
	// The capability exposes two tools: one to draw, one to report readiness.
	if len(tools) != 2 {
		t.Fatalf("expected image_generate and image_health, got %d tools", len(tools))
	}
	generate := imageTool(t, tools, "image_generate")
	_ = imageTool(t, tools, "image_health")
	if _, err := probe.NewToolRegistry(tools); err != nil {
		t.Fatalf("schema rejected by registry: %v", err)
	}
	// The profile enum must reflect the allow-list, never a free-form string.
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(generate.Parameters, &schema); err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	got := schema.Properties["profile"].Enum
	if len(got) != 2 || got[0] != "wai_v17" || got[1] != "z_anime_d8" {
		t.Fatalf("profile enum wrong: %v", got)
	}
	if schema.Properties["prompt"].Enum != nil {
		t.Fatal("prompt must stay free text")
	}
}

func TestImageToolAbsentWithoutBackend(t *testing.T) {
	if tools := (&Server{}).imageTools("role", "session"); tools != nil {
		t.Fatalf("no backend must mean no tool, got %+v", tools)
	}
	// A media store is also required: without it the picture could not be kept.
	if tools := (&Server{imagegen: stubImageClient(t)}).imageTools("role", "session"); tools != nil {
		t.Fatal("missing media store must disable the tool")
	}
}

func TestImageToolUsageStatesTheCost(t *testing.T) {
	server := stubServer(t)
	tool := imageTool(t, server.imageTools("role", "session"), "image_generate")
	for _, want := range []string{"asks for a picture", "10-60 seconds", "do not paste URLs"} {
		if !strings.Contains(tool.Usage, want) {
			t.Fatalf("usage should warn about %q: %s", want, tool.Usage)
		}
	}
}

func TestImageToolRejectsUnknownProfileBeforeRendering(t *testing.T) {
	// Validation must stop before the renderer is ever contacted.
	server := stubServer(t)
	tool := imageTool(t, server.imageTools("role", "session"), "image_generate")
	args := `{"prompt":"a whale","profile":"llada_t2i"}`
	_, err := tool.Execute(context.Background(), json.RawMessage(args))
	if err == nil {
		t.Fatal("profile outside the allow-list must be rejected")
	}
	if !strings.Contains(err.Error(), "不可用") {
		t.Fatalf("unexpected error: %v", err)
	}
}
