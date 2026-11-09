package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"chat-agent/internal/probe"
)

func TestAndroidUpdatePublication(t *testing.T) {
	dir := t.TempDir()
	body := []byte("test APK bytes; Android validates real package signatures")
	hash := sha256.Sum256(body)
	release := androidRelease{VersionCode: 3, VersionName: "0.3.0-debug", PackageName: "dev.chatagent.ai.debug", SHA256: hex.EncodeToString(hash[:]), Size: int64(len(body))}
	metadata, _ := json.Marshal(release)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), metadata, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.apk"), body, 0600); err != nil {
		t.Fatal(err)
	}
	agent := probe.DefaultConfig()
	agent.Demo = true
	const token = "update-test-token-at-least-32-characters"
	app, err := New(Config{Agent: agent, DataDir: t.TempDir(), AccessToken: token, AndroidReleaseDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(context.Background())
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	get := func(path string, authenticated bool) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		if authenticated {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		return res.StatusCode, data
	}
	path := "/api/updates/android/" + release.SHA256 + "/app.apk"
	for _, p := range []string{"/api/updates/android", path} {
		if code, _ := get(p, false); code != 401 {
			t.Fatal("unauthenticated update allowed")
		}
	}
	if code, data := get("/api/updates/android", true); code != 200 || !strings.Contains(string(data), path) {
		t.Fatal("manifest unavailable")
	}
	if code, data := get(path, true); code != 200 || string(data) != string(body) {
		t.Fatal("APK mismatch")
	}
	if code, _ := get("/api/updates/android/old/app.apk", true); code != 409 {
		t.Fatal("stale publication accepted")
	}
	body[0] ^= 1
	if err := os.WriteFile(filepath.Join(dir, "app.apk"), body, 0600); err != nil {
		t.Fatal(err)
	}
	if code, _ := get("/api/updates/android", true); code != 503 {
		t.Fatal("corrupt publication accepted")
	}
}
