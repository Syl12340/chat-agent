package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"chat-agent/internal/probe"
)

func TestModelConfigAdminLifecycleDoesNotReturnKey(t *testing.T) {
	const token = "model-config-test-owner-token-32-characters"
	agent := probe.DefaultConfig()
	agent.Demo = true
	agent.Model = "demo"
	app, err := New(Config{Agent: agent, DataDir: t.TempDir(), AccessToken: token})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = app.Close(ctx)
	})
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	do := func(method, path, body string, auth bool) (int, string) {
		req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if auth {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(data)
	}
	if status, _ := do("GET", "/api/models", "", false); status != http.StatusUnauthorized {
		t.Fatal("model configuration allowed without authentication")
	}
	const key = "server-side-secret"
	body := `{"id":"local_test","label":"Local test","base_url":"` + server.URL + `/v1","api_key":"` + key + `","model":"fixture","vision":false,"thinking":["off"]}`
	if status, response := do("POST", "/api/models", body, true); status != http.StatusCreated || strings.Contains(response, key) {
		t.Fatalf("create status=%d leaked=%v body=%s", status, strings.Contains(response, key), response)
	}
	if status, response := do("GET", "/api/models", "", true); status != http.StatusOK || strings.Contains(response, key) || !strings.Contains(response, `"configured":true`) {
		t.Fatalf("list status=%d body=%s", status, response)
	}
	update := `{"id":"local_test","label":"Updated","base_url":"` + server.URL + `/v1","model":"fixture","vision":true,"thinking":["off"]}`
	if status, response := do("PUT", "/api/models/local_test", update, true); status != http.StatusOK || strings.Contains(response, key) {
		t.Fatalf("update status=%d body=%s", status, response)
	}
	if route, ok := app.modelConfigs.Get("local_test"); !ok || route.APIKey != key || route.Label != "Updated" {
		t.Fatal("blank update did not retain server credential")
	}
	choice, err := app.selectChat("local_test", "off")
	if err != nil || choice.APIKey != key || choice.WireModel != "fixture" || choice.Model != "local_test" {
		t.Fatalf("route snapshot mismatch: %+v %v", choice, err)
	}
	if status, _ := do("DELETE", "/api/models/local_test", "", true); status != http.StatusOK {
		t.Fatalf("delete status=%d", status)
	}
}
