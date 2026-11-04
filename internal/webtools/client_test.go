package webtools

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

type resolverFunc func(context.Context, string) ([]net.IPAddr, error)

func (f resolverFunc) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return f(ctx, host)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// One bounded policy/protocol check. All responses are in-memory fixtures;
// mixed/private DNS is rejected before a socket can be opened.
func TestWebBoundary(t *testing.T) {
	for _, endpoint := range []string{"http://localhost:9993/search", "http://10.0.0.1:9993/search", "https://127.0.0.1:9993/search", "http://127.0.0.1:9993/other", "https://search.example/?q=hidden", "https://search.example/#hidden", "https://user@search.example/search"} {
		if _, err := New(Config{Enabled: true, SearchURL: endpoint}); err == nil {
			t.Fatalf("invalid search engine accepted: %s", endpoint)
		}
	}
	engine, err := New(Config{Enabled: true, SearchURL: "https://search.example/search"})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	badResult := false
	engine.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "search.example" || req.URL.Path != "/search" || req.URL.Query().Get("q") != "Go docs" {
			t.Fatalf("search engine request mismatch: %s", req.URL)
		}
		body := `<li class="b_algo"><h2><a href="https://pkg.go.dev/net/http">Docs</a></h2><p>Reference</p></li>`
		if badResult {
			body = `<li class="b_algo"><h2>nothing here</h2></li>`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/html"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	if results, err := engine.Search(context.Background(), "Go docs"); err != nil || len(results) != 1 {
		t.Fatalf("engine search failed: %v", err)
	}
	badResult = true
	if _, err := engine.Search(context.Background(), "Go docs"); err == nil {
		t.Fatal("a results page without parsable links must be an error, not empty results")
	}
	disabled, err := New(Config{})
	if err != nil || disabled.FetchEnabled() || disabled.SearchEnabled() {
		t.Fatal("web must default to disabled")
	}
	for _, value := range []string{"127.0.0.1", "10.0.0.1", "100.100.100.200", "169.254.169.254", "::1", "::ffff:127.0.0.1", "64:ff9b::a00:1", "2002:a00:1::1", "2001:db8::1"} {
		if public(net.ParseIP(value)) {
			t.Fatalf("unsafe address accepted: %s", value)
		}
	}
	if !public(net.ParseIP("8.8.8.8")) || !public(net.ParseIP("2606:4700:4700::1111")) {
		t.Fatal("public address rejected")
	}
	c, err := New(Config{Enabled: true, AllowedHosts: []string{"allowed.example"}, SearchURL: "https://search.example/search"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.resolver = resolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("10.0.0.1")}}, nil
	})
	if conn, err := c.dial(context.Background(), "tcp", "allowed.example:443"); err == nil {
		conn.Close()
		t.Fatal("mixed private/public DNS was not blocked")
	}
	calls := 0
	redirect := false
	c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if redirect {
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://127.0.0.1/"}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
		}
		body, kind := "<script>untrusted script</script><p>Reference &amp; text</p>"+strings.Repeat(" body", 4000), "text/html"
		if req.URL.Host == "search.example" {
			if req.URL.Query().Get("q") != "safe query" || req.Header.Get("Authorization") != "" {
				t.Error("search engine request mismatch")
			}
			var blocks strings.Builder
			blocks.WriteString(`<li class="b_algo"><h2><a href="javascript:alert(1)">bad</a></h2></li>`)
			for i := 0; i < 6; i++ {
				blocks.WriteString(`<li class="b_algo"><h2><a href="https://public.example/page` + string(rune('a'+i)) + `">Result</a></h2><p>reference</p></li>`)
			}
			body, kind = blocks.String(), "text/html"
		} else if req.Header.Get("Authorization") != "" {
			t.Error("search key sent to a fetched page")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{kind}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})
	for _, value := range []string{"http://allowed.example/", "https://allowed.example:8443/", "https://user:pass@allowed.example/", "https://not-allowed.example/", "https://127.0.0.1/", "https://allowed.example/#fragment"} {
		if _, err := c.Fetch(context.Background(), value); err == nil {
			t.Fatalf("unsafe URL accepted: %s", value)
		}
	}
	if calls != 0 {
		t.Fatal("blocked URL reached transport")
	}
	page, err := c.Fetch(context.Background(), "https://allowed.example/page")
	if err != nil || !page.Truncated || len(page.Content) > 12<<10 || strings.Contains(page.Content, "untrusted script") || !strings.Contains(page.Content, "Reference & text") {
		t.Fatalf("bounded text extraction failed: %v", err)
	}
	results, err := c.Search(context.Background(), "safe query")
	if err != nil || len(results) != 5 {
		t.Fatalf("bounded search failed: %v", err)
	}
	before := calls
	redirect = true
	if _, err := c.Fetch(context.Background(), "https://allowed.example/redirect"); err == nil || calls != before+1 {
		t.Fatal("redirect was followed")
	}
}
