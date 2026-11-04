package localsearch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeTransport func(*http.Request) (*http.Response, error)

func (f fakeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// One flow checks JSON + MCP, independent auth, cache reuse, bounded input,
// result filtering, genuine upstream failures and the uncached rate limit.
func TestLocalSearchFlow(t *testing.T) {
	token := strings.Repeat("s", 32)
	service, err := New(token, 5*time.Minute, 20)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	requests := 0
	broken := false
	service.http.Transport = fakeTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.Host != "cn.bing.com" || r.URL.Scheme != "https" || r.URL.Query().Get("format") != "rss" || r.Header.Get("Authorization") != "" {
			t.Fatal("upstream URL or credentials changed")
		}
		body := `<rss><channel><item><title>Go &amp; HTTP</title><link>https://pkg.go.dev/net/http#Request</link><description>&lt;b&gt;Docs&lt;/b&gt;</description></item><item><title>duplicate</title><link>https://pkg.go.dev/net/http</link></item><item><title>unsafe</title><link>http://127.0.0.1/admin</link></item></channel></rss>`
		if broken {
			body = `<html>Access blocked</html>`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/xml"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	server := httptest.NewServer(service.Handler())
	defer server.Close()
	call := func(path, body, auth, origin string) *http.Response {
		method := "GET"
		if body != "" {
			method = "POST"
		}
		req, _ := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("MCP-Protocol-Version", "2025-11-25")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	status := func(response *http.Response, want int) {
		t.Helper()
		defer response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("HTTP %d expected %d", response.StatusCode, want)
		}
	}
	status(call("/search?q=docs", "", "", ""), 401)
	status(call("/search?q=docs", "", token, "https://evil.example"), 403)
	response := call("/search?q=docs", "", token, "")
	var out Response
	json.NewDecoder(response.Body).Decode(&out)
	response.Body.Close()
	if response.StatusCode != 200 || len(out.Results) != 1 || out.Results[0].URL != "https://pkg.go.dev/net/http" || out.Results[0].Snippet != "Docs" {
		t.Fatal("filtered RSS result mismatch")
	}
	response = call("/mcp", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`, token, "")
	var rpc map[string]any
	json.NewDecoder(response.Body).Decode(&rpc)
	response.Body.Close()
	if rpc["error"] != nil || rpc["result"].(map[string]any)["protocolVersion"] != "2025-11-25" {
		t.Fatal("MCP initialize failed")
	}
	response = call("/mcp", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"web_search","arguments":{"query":"docs"}}}`, token, "")
	json.NewDecoder(response.Body).Decode(&rpc)
	response.Body.Close()
	if rpc["error"] != nil || rpc["result"].(map[string]any)["isError"] != false || requests != 1 {
		t.Fatal("MCP/cache shared flow failed")
	}
	status(call("/search?q=", "", token, ""), 400)
	broken = true
	status(call("/search?q=blocked", "", token, ""), 503)
	service.mu.Lock()
	service.requests = service.perMinute
	service.mu.Unlock()
	if _, err := service.Search(context.Background(), "another"); err != ErrBusy {
		t.Fatal("rate limit ignored")
	}
}
