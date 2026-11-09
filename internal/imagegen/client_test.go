package imagegen

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// pngBytes is a minimal but correctly-sniffed PNG header.
var pngBytes = []byte("\x89PNG\r\n\x1a\n0000")

// newTestClient builds a client against a fake runner: no Python required.
func newTestClient(t *testing.T, run runner) *Client {
	t.Helper()
	dir := t.TempDir()
	python := filepath.Join(dir, "python.exe")
	script := filepath.Join(dir, "imgctl.py")
	for _, path := range []string{python, script} {
		if err := os.WriteFile(path, []byte("stub"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	client := New(Config{
		Python: python, Script: script,
		Profiles: []string{"wai_v17", "z_anime_d8"}, DefaultProfile: "wai_v17",
		PollInterval: 1 * time.Millisecond,
	})
	if client == nil {
		t.Fatal("client should be enabled")
	}
	client.run = run
	return client
}

// fakeBridge answers submit/status/result in sequence and writes a real file.
func fakeBridge(t *testing.T, dir string, statusSequence []string) (runner, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	pending := statusSequence
	imagePath := filepath.Join(dir, "out_0001.png")
	if err := os.WriteFile(imagePath, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return func(ctx context.Context, args []string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, strings.Join(args, " "))
		switch args[0] {
		case "submit":
			return []byte(`{"job_id":"job-1","status":"queued","profile":"wai_v17","seed":123,"comfy_response":{}}`), nil
		case "status":
			state := "running"
			if len(pending) > 0 {
				state = pending[0]
				pending = pending[1:]
			}
			completed := state == "success"
			return []byte(`{"job_id":"job-1","state":"` + state + `","completed":` + boolText(completed) + `}`), nil
		case "result":
			return []byte(`{"job_id":"job-1","ready":true,"profile":"wai_v17","seed":123,"images":[{"path":` +
				strconvQuote(imagePath) + `,"filename":"out_0001.png"}]}`), nil
		}
		return nil, errors.New("unexpected command")
	}, &calls
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func strconvQuote(text string) string {
	return `"` + strings.ReplaceAll(text, `\`, `\\`) + `"`
}

func TestGenerateRunsTheFullSequence(t *testing.T) {
	dir := t.TempDir()
	run, calls := fakeBridge(t, dir, []string{"running", "success"})
	client := newTestClient(t, run)

	result, err := client.Generate(context.Background(), Request{
		Prompt: "a blue whale", Width: 768, Height: 768, Steps: 12, Seed: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.JobID != "job-1" || len(result.Images) != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Images[0].MIME != "image/png" || result.Images[0].Name != "out_0001.png" {
		t.Fatalf("image metadata wrong: %+v", result.Images[0])
	}
	joined := strings.Join(*calls, "\n")
	for _, want := range []string{"submit", "status job-1", "result job-1", "--profile wai_v17", "--prompt a blue whale"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in calls:\n%s", want, joined)
		}
	}
	if strings.Count(joined, "status") != 2 {
		t.Fatalf("should poll until success, got:\n%s", joined)
	}
}

func TestGenerateReportsBridgeRejection(t *testing.T) {
	client := newTestClient(t, func(ctx context.Context, args []string) ([]byte, error) {
		return []byte(`{"ok":false,"error":"BridgeError","message":"Only 7.63 GB RAM is free; profile requires at least 8.00 GB before submission"}`), errors.New("exit status 1")
	})
	_, err := client.Generate(context.Background(), Request{Prompt: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "GB RAM") {
		t.Fatalf("bridge message should reach the caller verbatim: %v", err)
	}
}

func TestGenerateFailsWhenJobFails(t *testing.T) {
	dir := t.TempDir()
	run, _ := fakeBridge(t, dir, []string{"running", "failed"})
	client := newTestClient(t, run)
	if _, err := client.Generate(context.Background(), Request{Prompt: "x"}); err == nil {
		t.Fatal("failed job should error")
	}
}

func TestGenerateRejectsUnknownProfileAndBadParams(t *testing.T) {
	client := newTestClient(t, func(ctx context.Context, args []string) ([]byte, error) {
		t.Fatal("validation must not reach the bridge")
		return nil, nil
	})
	cases := map[string]Request{
		"unknown profile": {Profile: "llada_t2i", Prompt: "x"},
		"empty prompt":    {Prompt: "  "},
		"prompt too big":  {Prompt: strings.Repeat("x", maxPromptChars+1)},
		"width too small": {Prompt: "x", Width: 64},
		"width too big":   {Prompt: "x", Width: 4096},
		"steps too big":   {Prompt: "x", Steps: 99},
		"cfg too big":     {Prompt: "x", CFG: 91},
	}
	for name, req := range cases {
		if _, err := client.Generate(context.Background(), req); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}
}

func TestGenerateTimesOutWhilePolling(t *testing.T) {
	client := newTestClient(t, func(ctx context.Context, args []string) ([]byte, error) {
		switch args[0] {
		case "submit":
			return []byte(`{"job_id":"job-1"}`), nil
		default:
			return []byte(`{"job_id":"job-1","state":"running","completed":false}`), nil
		}
	})
	client.timeout = 60 * time.Millisecond
	client.poll = 10 * time.Millisecond
	_, err := client.Generate(context.Background(), Request{Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("expected timeout, got %v", err)
	}
}

func TestGenerateRejectsNonImageOutput(t *testing.T) {
	dir := t.TempDir()
	textPath := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(textPath, []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := newTestClient(t, func(ctx context.Context, args []string) ([]byte, error) {
		switch args[0] {
		case "submit":
			return []byte(`{"job_id":"job-1"}`), nil
		case "status":
			return []byte(`{"job_id":"job-1","state":"success","completed":true}`), nil
		default:
			return []byte(`{"job_id":"job-1","ready":true,"images":[{"path":` + strconvQuote(textPath) + `,"filename":"notes.txt"}]}`), nil
		}
	})
	if _, err := client.Generate(context.Background(), Request{Prompt: "x"}); err == nil {
		t.Fatal("non-image output must be rejected")
	}
}

func TestProfilesAndDefaults(t *testing.T) {
	client := newTestClient(t, nil)
	profiles := client.Profiles()
	if len(profiles) != 2 || profiles[0] != "wai_v17" || profiles[1] != "z_anime_d8" {
		t.Fatalf("profiles should be sorted: %v", profiles)
	}
	if client.DefaultProfile() != "wai_v17" {
		t.Fatalf("default profile wrong: %s", client.DefaultProfile())
	}
	var nilClient *Client
	if _, err := nilClient.Generate(context.Background(), Request{Prompt: "x"}); err == nil {
		t.Fatal("nil client should error")
	}
	if nilClient.Profiles() != nil || nilClient.DefaultProfile() != "" {
		t.Fatal("nil client accessors should be safe")
	}
}

func TestNewRequiresExistingPaths(t *testing.T) {
	if New(Config{Python: "missing", Script: "missing", Profiles: []string{"a"}}) != nil {
		t.Fatal("missing interpreter must disable the client")
	}
	dir := t.TempDir()
	python := filepath.Join(dir, "py")
	_ = os.WriteFile(python, []byte("x"), 0o600)
	if New(Config{Python: python, Script: filepath.Join(dir, "nope.py"), Profiles: []string{"a"}}) != nil {
		t.Fatal("missing script must disable the client")
	}
	if New(Config{Python: python, Script: python}) != nil {
		t.Fatal("empty profile list must disable the client")
	}
}

func TestExtractJSONIgnoresNoise(t *testing.T) {
	raw := []byte("some warning\r\n{\"ok\":true}\r\ntrailing")
	got := extractJSON(raw)
	if string(got) != `{"ok":true}` {
		t.Fatalf("unexpected extraction: %q", string(got))
	}
	if extractJSON([]byte("no json here")) != nil {
		t.Fatal("should return nil without braces")
	}
}
