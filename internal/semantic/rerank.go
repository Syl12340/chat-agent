// Package semantic reorders candidate documents with the platform's rerank
// model. It is optional: every method reports whether it succeeded, and callers
// keep their local ordering when it does not.
package semantic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	Timeout time.Duration
}

type Client struct {
	endpoint string
	key      string
	model    string
	http     *http.Client

	mu       sync.Mutex
	failures int
	openTill time.Time
}

// breakerCooldown is how long the client stops trying after repeated failures,
// so an unavailable service costs the local ranking nothing but a map lookup.
const breakerCooldown = 60 * time.Second

// breakerThreshold is the consecutive-failure count that opens the breaker.
const breakerThreshold = 2

// maxDocuments bounds one rerank request; the platform caps each document at
// 8192 characters and this feature must never turn a chat turn into a bulk job.
const maxDocuments = 48

func New(c Config) *Client {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" || c.APIKey == "" || strings.TrimSpace(c.Model) == "" {
		return nil
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" {
		return nil
	}
	timeout := c.Timeout
	if timeout <= 0 || timeout > 10*time.Second {
		timeout = 2500 * time.Millisecond
	}
	return &Client{
		endpoint: base + "/rerank",
		key:      c.APIKey,
		model:    c.Model,
		http:     &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

func (c *Client) Enabled() bool { return c != nil }

type rerankResult struct {
	Index int     `json:"index"`
	Score float64 `json:"relevance_score"`
}

// Rerank returns document indexes ordered by relevance, best first. The second
// return is false whenever the caller should keep its own ordering — the service
// is unavailable, slow, or answered with something unusable.
func (c *Client) Rerank(query string, documents []string, limit int) ([]int, bool) {
	return c.RerankContext(context.Background(), query, documents, limit)
}

func (c *Client) RerankContext(ctx context.Context, query string, documents []string, limit int) ([]int, bool) {
	if ctx.Err() != nil {
		return nil, false
	}
	if c == nil || strings.TrimSpace(query) == "" || len(documents) < 2 || limit < 1 {
		return nil, false
	}
	if !c.allow() {
		return nil, false
	}
	indexes, ok := c.request(ctx, query, documents, limit)
	if ctx.Err() == nil {
		c.record(ok)
	}
	return indexes, ok
}

// allow reports whether the breaker still permits a request.
func (c *Client) allow() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.openTill.IsZero() || time.Now().After(c.openTill)
}

// record tracks consecutive failures and opens or resets the breaker.
func (c *Client) record(ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ok {
		c.failures, c.openTill = 0, time.Time{}
		return
	}
	c.failures++
	if c.failures >= breakerThreshold {
		c.openTill = time.Now().Add(breakerCooldown)
		c.failures = 0
	}
}

func (c *Client) request(parent context.Context, query string, documents []string, limit int) ([]int, bool) {
	docs := documents
	if len(docs) > maxDocuments {
		docs = docs[:maxDocuments]
	}
	payload := map[string]any{
		"model":            c.model,
		"query":            boundText(query, 1024),
		"documents":        make([]string, len(docs)),
		"top_n":            min(limit, len(docs)),
		"return_documents": false,
	}
	for i, doc := range docs {
		payload["documents"].([]string)[i] = boundText(doc, 4096)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(parent, c.http.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.key)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var parsed struct {
		Results []rerankResult `json:"results"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&parsed) != nil || len(parsed.Results) == 0 {
		return nil, false
	}
	ordered := make([]rerankResult, 0, len(parsed.Results))
	for _, result := range parsed.Results {
		if result.Index >= 0 && result.Index < len(docs) && result.Score > 0 {
			ordered = append(ordered, result)
		}
	}
	if len(ordered) == 0 {
		return nil, false
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Score > ordered[j].Score })
	out := make([]int, 0, len(ordered))
	for _, result := range ordered[:min(limit, len(ordered))] {
		out = append(out, result.Index)
	}
	return out, len(out) > 0
}

// boundText keeps both the head and the tail so a long record's subject and its
// latest correction both reach the model.
func boundText(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit/2]) + " " + string(runes[len(runes)-limit/2:])
}

var errDisabled = errors.New("semantic rerank is disabled")

// Disabled reports the package-level reason a client returned nil, for logging.
func Disabled() error { return errDisabled }
