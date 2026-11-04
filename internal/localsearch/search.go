// Package localsearch wraps a fixed public RSS search upstream with bounded
// caching and local authenticated JSON/MCP interfaces. It has no account API key.
package localsearch

import (
	"context"
	"encoding/xml"
	"errors"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"pocket-agent/internal/webtools"
)

var ErrBusy = errors.New("search request limit reached; try later")
var ErrQuery = errors.New("query must contain 1..1024 UTF-8 bytes")
var markup = regexp.MustCompile(`<[^>]*>`)

type Response struct {
	Results   []webtools.Result `json:"results"`
	Source    string            `json:"source"`
	Cached    bool              `json:"cached"`
	FetchedAt time.Time         `json:"fetched_at"`
}
type cached struct {
	response Response
	expires  time.Time
}
type Service struct {
	http      *http.Client
	tokenHash [32]byte
	mu        sync.Mutex
	cache     map[string]cached
	ttl       time.Duration
	window    time.Time
	requests  int
	perMinute int
	active    chan struct{}
}

func New(token string, ttl time.Duration, perMinute int) (*Service, error) {
	if len(token) < 32 || ttl < 0 || ttl > time.Hour || perMinute < 1 || perMinute > 60 {
		return nil, errors.New("search requires a 32+ character token, 0..1h cache TTL and 1..60 requests/minute")
	}
	return newService(token, ttl, perMinute), nil
}
func (s *Service) Close() { s.http.CloseIdleConnections() }

func clean(value string, n int) string {
	value = strings.Join(strings.Fields(html.UnescapeString(markup.ReplaceAllString(value, " "))), " ")
	if len(value) > n {
		value = value[:n]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}

func (s *Service) Search(ctx context.Context, query string) (Response, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 1024 || !utf8.ValidString(query) {
		return Response{}, ErrQuery
	}
	now := time.Now()
	s.mu.Lock()
	if entry, ok := s.cache[query]; ok && now.Before(entry.expires) {
		s.mu.Unlock()
		out := entry.response
		out.Cached = true
		return out, nil
	}
	if now.Sub(s.window) >= time.Minute {
		s.window = now
		s.requests = 0
	}
	if s.requests >= s.perMinute {
		s.mu.Unlock()
		return Response{}, ErrBusy
	}
	select {
	case s.active <- struct{}{}:
	default:
		s.mu.Unlock()
		return Response{}, ErrBusy
	}
	s.requests++
	s.mu.Unlock()
	defer func() { <-s.active }()
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	upstream := "https://cn.bing.com/search?" + url.Values{"q": {query}, "format": {"rss"}, "count": {"5"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream, nil)
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("User-Agent", "PocketAgentSearch/0.1")
	req.Header.Set("Accept", "application/rss+xml, application/xml, text/xml")
	response, err := s.http.Do(req)
	if err != nil {
		return Response{}, errors.New("public search upstream unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Response{}, errors.New("public search upstream rejected the request")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (512<<10)+1))
	if err != nil || len(data) > 512<<10 {
		return Response{}, errors.New("invalid or oversized upstream response")
	}
	var feed struct {
		XMLName xml.Name `xml:"rss"`
		Channel struct {
			Items []struct {
				Title       string `xml:"title"`
				URL         string `xml:"link"`
				Description string `xml:"description"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if xml.Unmarshal(data, &feed) != nil || feed.XMLName.Local != "rss" {
		return Response{}, errors.New("upstream did not return a valid RSS search response")
	}
	out := Response{Results: []webtools.Result{}, Source: "bing-rss", FetchedAt: time.Now().UTC()}
	seen := map[string]bool{}
	for _, item := range feed.Channel.Items {
		parsed, err := url.Parse(strings.TrimSpace(item.URL))
		if err != nil {
			continue
		}
		parsed.Fragment = ""
		link, err := webtools.ResultURL(parsed.String())
		if err != nil || seen[link] {
			continue
		}
		title := clean(item.Title, 512)
		if title == "" {
			continue
		}
		seen[link] = true
		out.Results = append(out.Results, webtools.Result{Title: title, URL: link, Snippet: clean(item.Description, 2048)})
		if len(out.Results) == 5 {
			break
		}
	}
	s.mu.Lock()
	if s.ttl > 0 {
		for key, entry := range s.cache {
			if now.After(entry.expires) {
				delete(s.cache, key)
			}
		}
		if len(s.cache) >= 128 {
			oldest := ""
			var deadline time.Time
			for key, entry := range s.cache {
				if oldest == "" || entry.expires.Before(deadline) {
					oldest, deadline = key, entry.expires
				}
			}
			delete(s.cache, oldest)
		}
		s.cache[query] = cached{out, time.Now().Add(s.ttl)}
	}
	s.mu.Unlock()
	return out, nil
}
