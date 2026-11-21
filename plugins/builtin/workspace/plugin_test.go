package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"chat-agent/internal/pluginapi"
)

// open creates a plugin rooted in a fresh temp dir with an approved host. The
// tests exercise the boundary, so they run the real constructor rather than a
// hand-built struct.
func open(t *testing.T) (*Plugin, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "ws")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	plugin, err := New(root, data, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := plugin.Start(context.Background(), granted{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plugin.Stop(context.Background()) })
	return plugin, root
}

// granted is a host that approves exactly the workspace permission.
type granted struct{}

func (granted) Logger() *log.Logger            { return log.New(io.Discard, "", 0) }
func (granted) DataDir() string                { return "" }
func (granted) Allowed(permission string) bool { return permission == Permission }

func call(t *testing.T, handler pluginapi.Handler, args string) (pluginapi.Result, error) {
	t.Helper()
	return handler(context.Background(), json.RawMessage(args))
}

// The path filter is the first line of defence: these never reach the filesystem.
func TestAllowedPathRejectsEscapes(t *testing.T) {
	for _, path := range []string{
		"..", "../etc/passwd", "a/../../etc/passwd", "/etc/passwd",
		"a//b", "./a", ".hidden", "a/.hidden", "",
		"c:\\windows", "a\x00b", strings.Repeat("a", 300),
	} {
		if allowedPath(path) {
			t.Fatalf("%q must be rejected", path)
		}
	}
	for _, path := range []string{".", "notes", "notes/today.md", "a/b/c.txt"} {
		if !allowedPath(path) {
			t.Fatalf("%q should be allowed", path)
		}
	}
}

// A symlink inside the workspace must not become a way out of it.
func TestReadRefusesSymlinkEscape(t *testing.T) {
	plugin, root := open(t)
	outside := filepath.Join(filepath.Dir(root), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := call(t, plugin.read, `{"workspace":"server","path":"link.txt"}`); err == nil {
		t.Fatal("a symlink pointing outside the workspace must not be readable")
	}
	// It must not be listed either: a link the agent cannot follow is noise.
	result, err := call(t, plugin.list, `{"workspace":"server","path":"."}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Output, "link.txt") {
		t.Fatal("symlinks must not appear in the listing")
	}
}

// The constructor refuses roots that overlap host data, because a workspace is
// only a boundary if it excludes the things worth protecting.
func TestNewRejectsProtectedRoots(t *testing.T) {
	data := t.TempDir()
	for _, root := range []string{"/", "/etc", "/etc/ssl", "/root", "/opt/chat-agent", "/proc", data} {
		if _, err := New(root, data, ""); err == nil {
			t.Fatalf("root %q must be rejected", root)
		}
	}
}

func TestNewRequiresAbsoluteRoot(t *testing.T) {
	if _, err := New("relative/path", t.TempDir(), ""); err == nil {
		t.Fatal("a relative workspace root must be rejected")
	}
}

// The gap this closes: read used to return only the body while write demanded the
// exact hash of what it replaces, so an existing file could never be edited - the
// failure message said to read it again, and reading again gave the same hashless
// text. This walks the whole loop the way a caller would.
func TestReadThenWriteClosesTheLoop(t *testing.T) {
	plugin, root := open(t)
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("version one"), 0o600); err != nil {
		t.Fatal(err)
	}
	read, err := call(t, plugin.read, `{"workspace":"server","path":"note.txt"}`)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("version one"))
	if want := fmt.Sprintf("sha256: %x", digest); !strings.Contains(read.Output, want) {
		t.Fatalf("read must publish the hash write expects, got %q", read.Output)
	}
	write, err := call(t, plugin.write, fmt.Sprintf(`{"workspace":"server","path":"note.txt","content":"version two","expected_sha256":"%x"}`, digest))
	if err != nil {
		t.Fatalf("writing with the hash from read must succeed: %v", err)
	}
	if !strings.Contains(write.Output, "version two") && !strings.Contains(write.Output, "note.txt") {
		t.Logf("write result: %q", write.Output)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(saved) != "version two" {
		t.Fatalf("file content = %q", saved)
	}
}

func TestReadEnforcesTextLimits(t *testing.T) {
	plugin, root := open(t)
	if err := os.WriteFile(filepath.Join(root, "ok.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := call(t, plugin.read, `{"workspace":"server","path":"ok.txt"}`)
	if err != nil || !strings.HasPrefix(result.Output, "hello") {
		t.Fatalf("plain text should read: %q %v", result.Output, err)
	}
	// The digest workspace_write requires has to come back with the body, or an
	// edit of an existing file can never be written.
	if want := fmt.Sprintf("sha256: %x", sha256.Sum256([]byte("hello"))); !strings.Contains(result.Output, want) {
		t.Fatalf("read must publish its write precondition, got %q", result.Output)
	}

	if err := os.WriteFile(filepath.Join(root, "big.txt"), make([]byte, limit+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, plugin.read, `{"workspace":"server","path":"big.txt"}`); err == nil {
		t.Fatal("an oversized file must be refused, not truncated")
	}

	if err := os.WriteFile(filepath.Join(root, "bin.txt"), []byte{0xff, 0xfe, 0x00}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, plugin.read, `{"workspace":"server","path":"bin.txt"}`); err == nil {
		t.Fatal("non-UTF-8 content must be refused")
	}
}

func TestWriteRequiresCurrentHashAndReplacesAtomically(t *testing.T) {
	plugin, root := open(t)
	path := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("before"))
	args := fmt.Sprintf(`{"workspace":"server","path":"notes.txt","content":"after","expected_sha256":"%x"}`, digest)
	result, err := call(t, plugin.write, args)
	if err != nil || !strings.Contains(result.Output, `"status":"written"`) {
		t.Fatalf("write result=%q err=%v", result.Output, err)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "after" {
		t.Fatalf("written file=%q err=%v", raw, err)
	}
	if _, err := call(t, plugin.write, args); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale hash was accepted: %v", err)
	}
	if _, err := call(t, plugin.write, `{"workspace":"server","path":"new/fresh.txt","content":"created","expected_sha256":"missing"}`); err != nil {
		t.Fatalf("new file: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(root, "new", "fresh.txt")); err != nil || string(raw) != "created" {
		t.Fatalf("new file=%q err=%v", raw, err)
	}
	large := strings.Repeat("x", (256<<10)+1)
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(large), 0o600); err != nil {
		t.Fatal(err)
	}
	largeDigest := sha256.Sum256([]byte(large))
	largeArgs := fmt.Sprintf(`{"workspace":"server","path":"large.txt","content":"small","expected_sha256":"%x"}`, largeDigest)
	if _, err := call(t, plugin.write, largeArgs); err == nil {
		t.Fatal("oversized existing file was read for replacement")
	}
}

func TestDownloadReceiptBindsExactBytes(t *testing.T) {
	_, root := open(t)
	path := filepath.Join(root, "report.bin")
	if err := os.WriteFile(path, []byte{1, 2, 3, 4}, 0o600); err != nil {
		t.Fatal(err)
	}
	file, info, _, digest, err := OpenDownload(root, "report.bin", "")
	if err != nil || info == nil || info.Size() != 4 || len(digest) != 64 {
		t.Fatalf("offer failed: info=%v digest=%q err=%v", info, digest, err)
	}
	_ = file.Close()
	if err = os.WriteFile(path, []byte{4, 3, 2, 1}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err = OpenDownload(root, "report.bin", digest); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed file reused an old receipt: %v", err)
	}
}

// A directory cannot be read as a file, and a file cannot be listed as one.
func TestReadAndListKeepTheirRoles(t *testing.T) {
	plugin, root := open(t)
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, plugin.read, `{"workspace":"server","path":"sub"}`); err == nil {
		t.Fatal("reading a directory must fail")
	}
	if _, err := call(t, plugin.list, `{"workspace":"server","path":"missing"}`); err == nil {
		t.Fatal("listing a missing directory must fail")
	}
}

// Only the configured action names may run, and only with a recorded operation.
func TestExecuteRefusesUnconfiguredAction(t *testing.T) {
	plugin, _ := open(t)
	if _, err := call(t, plugin.execute, `{"workspace":"server","action":"rm-rf"}`); err == nil {
		t.Fatal("an action outside the allow-list must not run")
	}
	// No operation id in the context: the call is not part of a recorded run.
	if _, err := call(t, plugin.execute, `{"workspace":"server","action":""}`); err == nil {
		t.Fatal("an empty action must not run")
	}
}

// Claiming to need isolation must fail closed rather than run unconfined.
func TestExecuteFailsClosedOnSandboxRequirement(t *testing.T) {
	plugin, _ := open(t)
	_, err := call(t, plugin.execute, `{"workspace":"server","action":"anything","require_sandbox":true}`)
	if err == nil || !strings.Contains(err.Error(), "SANDBOX_UNAVAILABLE") {
		t.Fatalf("a sandbox requirement must fail closed, got %v", err)
	}
}

func TestActionsJSONIsValidated(t *testing.T) {
	data := t.TempDir()
	root := filepath.Join(t.TempDir(), "ws")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`not json`,
		`{"":["/bin/ls"]}`,
		`{"ok":[]}`,
		`{"ok":["relative"]}`,
		`{"ok":["/bin/ls","/bin/cat","/bin/echo","/bin/pwd","/bin/true","/bin/false","/bin/date","/bin/hostname","/bin/id","/bin/uname","/bin/whoami","/bin/uptime","/bin/free","/bin/env","/bin/ls","/bin/ls","/bin/ls","/bin/ls","/bin/ls","/bin/ls","/bin/ls","/bin/ls","/bin/ls","/bin/ls","/bin/ls"]}`,
	} {
		if _, err := New(root, data, raw); err == nil {
			t.Fatalf("actions %q must be rejected", raw)
		}
	}
}
