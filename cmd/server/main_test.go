package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDesktopHelpersKeepAccessLocalAndPersistent(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8765", "[::1]:8765", "localhost:8765"} {
		if !loopbackAddress(address) {
			t.Fatalf("expected loopback address %q", address)
		}
	}
	for _, address := range []string{"0.0.0.0:8765", "192.0.2.1:8765", "missing-port"} {
		if loopbackAddress(address) {
			t.Fatalf("accepted non-loopback address %q", address)
		}
	}

	data := t.TempDir()
	first, err := desktopAccessToken(data)
	if err != nil {
		t.Fatal(err)
	}
	second, err := desktopAccessToken(data)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(first) != 64 {
		t.Fatalf("desktop token is not stable: lengths %d and %d", len(first), len(second))
	}
	raw, err := os.ReadFile(filepath.Join(data, "desktop-access.token"))
	if err != nil || string(raw) != first+"\n" {
		t.Fatalf("desktop token was not persisted correctly: %v", err)
	}
}
