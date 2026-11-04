package semantic

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestDisabledClientStaysOff(t *testing.T) {
	for _, config := range []Config{
		{},
		{BaseURL: "https://example.test/v1"},
		{BaseURL: "https://example.test/v1", APIKey: "k"},
		{BaseURL: "ftp://example.test/v1", APIKey: "k", Model: "m"},
		{BaseURL: "https://user:pass@example.test/v1", APIKey: "k", Model: "m"},
	} {
		if client := New(config); client != nil {
			t.Fatalf("incomplete or unsafe configuration produced a client: %+v", config)
		}
	}
	var nilClient *Client
	if _, ok := nilClient.Rerank("q", []string{"a", "b"}, 1); ok {
		t.Fatal("a nil client must report failure")
	}
}

func TestRerankOrdersByScore(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rerank" || r.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("unexpected request %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body struct {
			Model     string   `json:"model"`
			Query     string   `json:"query"`
			Documents []string `json:"documents"`
			TopN      int      `json:"top_n"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Model != "ecnu-rerank" || body.Query != "图书馆" || len(body.Documents) != 3 {
			t.Errorf("unexpected payload %+v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
			{"index": 1, "relevance_score": 0.91},
			{"index": 2, "relevance_score": 0.42},
			{"index": 0, "relevance_score": 0.03},
		}})
	}))
	defer server.Close()
	client := New(Config{BaseURL: server.URL + "/v1", APIKey: "key", Model: "ecnu-rerank"})
	order, ok := client.Rerank("图书馆", []string{"a", "b", "c"}, 2)
	if !ok || len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatalf("unexpected order %v ok=%v", order, ok)
	}
}

func TestRerankFailsSoftAndBreaksTheCircuit(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client := New(Config{BaseURL: server.URL + "/v1", APIKey: "key", Model: "ecnu-rerank"})
	for i := 0; i < 2; i++ {
		if _, ok := client.Rerank("q", []string{"a", "b"}, 1); ok {
			t.Fatal("a failing service must report failure")
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected 2 upstream attempts, got %d", got)
	}
	// The breaker is now open: no further requests leave the process.
	for i := 0; i < 5; i++ {
		if _, ok := client.Rerank("q", []string{"a", "b"}, 1); ok {
			t.Fatal("an open breaker must report failure")
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("open breaker still called upstream: %d", got)
	}
	// Once the cooldown lapses the client tries again.
	client.mu.Lock()
	client.openTill = time.Now().Add(-time.Second)
	client.mu.Unlock()
	if _, ok := client.Rerank("q", []string{"a", "b"}, 1); ok {
		t.Fatal("service is still failing")
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("client did not retry after cooldown: %d", got)
	}
}

func TestRerankRejectsUnusableAnswers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()
	client := New(Config{BaseURL: server.URL + "/v1", APIKey: "key", Model: "ecnu-rerank"})
	if _, ok := client.Rerank("q", []string{"a", "b"}, 1); ok {
		t.Fatal("an empty result set must fall back to the local ordering")
	}
	if _, ok := client.Rerank("  ", []string{"a", "b"}, 1); ok {
		t.Fatal("an empty query must not reach the service")
	}
	if _, ok := client.Rerank("q", []string{"only"}, 1); ok {
		t.Fatal("a single candidate needs no reranking")
	}
}
