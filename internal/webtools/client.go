// Package webtools provides opt-in, bounded HTTPS tools with pinned public DNS.
package webtools

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

type Config struct {
	Enabled      bool
	AllowedHosts []string
	// SearchURL is a public search engine results page (HTML), for example
	// https://cn.bing.com/search . It is read and parsed directly; no wrapper.
	SearchURL string
}
type Resolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}
type Client struct {
	hosts     map[string]bool
	searchURL string
	http      *http.Client
	resolver  Resolver
}
type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

var domain = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)
var deny = []netip.Prefix{}

func init() {
	for _, p := range []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "2001::/23", "2001:db8::/32", "2002::/16"} {
		deny = append(deny, netip.MustParsePrefix(p))
	}
}
func public(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		return false
	}
	if addr.Is6() && !netip.MustParsePrefix("2000::/3").Contains(addr) {
		return false
	}
	for _, prefix := range deny {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}
func hostName(value string) bool {
	return len(value) <= 253 && domain.MatchString(value) && strings.Contains(value, ".") && !strings.Contains(value, "..") && net.ParseIP(value) == nil
}
func parseURL(value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
		return nil, failure(StagePolicy, CodeInvalidURL, "only HTTPS port 443 URLs without credentials or fragments are allowed")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if !hostName(host) {
		return nil, failure(StagePolicy, CodeInvalidURL, "a valid DNS hostname is required, not an address or a bare name")
	}
	u.Host = host
	return u, nil
}

func New(c Config) (*Client, error) {
	if !c.Enabled {
		return nil, nil
	}
	client := &Client{hosts: map[string]bool{}, resolver: net.DefaultResolver}
	for _, value := range c.AllowedHosts {
		host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
		if host == "" {
			continue
		}
		if !hostName(host) {
			return nil, errors.New("web allowlist must contain exact DNS names")
		}
		client.hosts[host] = true
	}
	if c.SearchURL != "" {
		u, err := parseURL(c.SearchURL)
		if err != nil || u.RawQuery != "" {
			return nil, errors.New("search engine must be a plain HTTPS results-page URL")
		}
		client.searchURL = u.String()
	}
	if len(client.hosts) == 0 && client.searchURL == "" {
		return nil, errors.New("enabled web tools require a search engine or allowed hosts")
	}
	client.http = client.publicHTTP()
	return client, nil
}

// NewPublicHTTPClient shares the public-DNS/HTTPS boundary with local search's
// fixed upstream. The loopback exception above never changes this transport.
func NewPublicHTTPClient() *http.Client { return (&Client{resolver: net.DefaultResolver}).publicHTTP() }

func (client *Client) publicHTTP() *http.Client {
	transport := &http.Transport{Proxy: nil, DialContext: client.dial, TLSHandshakeTimeout: 4 * time.Second, ResponseHeaderTimeout: 5 * time.Second, IdleConnTimeout: 30 * time.Second, MaxIdleConns: 4, MaxConnsPerHost: 2, MaxResponseHeaderBytes: 16 << 10}
	return &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errRedirectBlocked }}
}

// errRedirectBlocked is the sentinel the redirect policy returns; the request path
// turns it into a named failure so a caller can tell "the page moved" from "the
// address is unsafe".
var errRedirectBlocked = errors.New("redirects are disabled")

func (c *Client) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" {
		return nil, failure(StagePolicy, CodePolicyDenied, "only port 443 is reachable")
	}
	addresses, err := c.resolver.LookupIPAddr(ctx, host)
	if err != nil || len(addresses) == 0 || len(addresses) > 16 {
		return nil, &Failure{Stage: StageTransport, Code: CodeDNSFailed, Message: "the host name did not resolve", Retryable: true, Host: host}
	}
	for _, address := range addresses {
		if !public(address.IP) {
			return nil, &Failure{Stage: StagePolicy, Code: CodePrivateDestination, Message: "the host resolves to a private or reserved address, which is never fetched", Host: host}
		}
	}
	dialer := net.Dialer{Timeout: 3 * time.Second}
	for _, address := range addresses {
		conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(address.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
	}
	return nil, &Failure{Stage: StageTransport, Code: CodeConnectTimeout, Message: "could not open a connection to the site", Retryable: true, Host: host}
}
func (c *Client) FetchEnabled() bool  { return c != nil && (len(c.hosts) > 0 || c.searchURL != "") }
func (c *Client) SearchEnabled() bool { return c != nil && c.searchURL != "" }
func (c *Client) read(ctx context.Context, u *url.URL, key string) ([]byte, string, bool, error) {
	return c.readAs(ctx, u, key, "ChatAgent/0.3")
}

func (c *Client) readAs(ctx context.Context, u *url.URL, key, agent string) ([]byte, string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", false, errors.New("invalid web request")
	}
	req.Header.Set("User-Agent", agent)
	req.Header.Set("Accept", "text/plain, text/html, application/json")
	if agent == browserAgent {
		req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", false, context.Cause(ctx)
		}
		return nil, "", false, transportFailure(err, u.Hostname())
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", false, statusFailure(resp, u.Hostname(), time.Now())
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (256<<10)+1))
	if err != nil {
		return nil, "", false, &Failure{Stage: StageTransport, Code: CodeConnectTimeout, Message: "the connection broke while reading the page", Retryable: true, Host: u.Hostname()}
	}
	truncated := len(data) > 256<<10
	if truncated {
		data = data[:256<<10]
	}
	return data, strings.ToLower(strings.Split(resp.Header.Get("Content-Type"), ";")[0]), truncated, nil
}

// transportFailure names what went wrong before any bytes arrived. The dialer
// already classifies DNS and destination problems, so those are passed through;
// what is left is a redirect, a TLS problem, or a timeout.
func transportFailure(err error, host string) *Failure {
	var named *Failure
	if errors.As(err, &named) {
		return named
	}
	if errors.Is(err, errRedirectBlocked) {
		return &Failure{Stage: StagePolicy, Code: CodeRedirectBlocked, Message: "the site redirects elsewhere, and redirects are not followed; open the final address instead", Host: host}
	}
	var netErr net.Error
	timeout := errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout())
	var recordErr tls.RecordHeaderError
	var authorityErr x509.UnknownAuthorityError
	var hostnameErr x509.HostnameError
	var certificateErr x509.CertificateInvalidError
	if errors.As(err, &recordErr) || errors.As(err, &authorityErr) || errors.As(err, &hostnameErr) || errors.As(err, &certificateErr) {
		return &Failure{Stage: StageTransport, Code: CodeTLSFailed, Message: "the site's certificate could not be verified", Host: host}
	}
	if timeout {
		return &Failure{Stage: StageTransport, Code: CodeConnectTimeout, Message: "the site did not answer in time", Retryable: true, Host: host}
	}
	return &Failure{Stage: StageTransport, Code: CodeUnknown, Message: "the request could not be completed", Host: host}
}

func bounded(text string, n int) string {
	if len(text) <= n {
		return text
	}
	text = text[:n]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text
}
func (c *Client) Fetch(ctx context.Context, value string) (Page, error) {
	if !c.FetchEnabled() {
		return Page{}, failure(StagePolicy, CodePolicyDenied, "web fetch is disabled on this host")
	}
	u, err := parseURL(value)
	if err != nil {
		return Page{}, err
	}
	// An empty allowlist means the whole public HTTPS surface is readable; the
	// transport still pins DNS, rejects private/reserved addresses and blocks
	// redirects. A configured list narrows that surface explicitly.
	if len(c.hosts) > 0 && !c.hosts[u.Hostname()] {
		return Page{}, &Failure{Stage: StagePolicy, Code: CodePolicyDenied, Message: "this host is not in the fetch allowlist", Host: u.Hostname()}
	}
	data, kind, truncated, err := c.read(ctx, u, "")
	if err != nil {
		return Page{}, err
	}
	if kind == "text/html" {
		// Structure is parsed, not stripped: the headings, links and tables are what
		// make the next step possible.
		page := ParsePage(u.String(), data, truncated)
		// A page that asks a human to prove they are one has text, and returning that
		// text as the article would be a confident lie.
		if challengePage(page.Title, page.Content) {
			return Page{}, &Failure{Stage: StageContent, Code: CodeChallengeRequired, Message: "the site returned a verification page instead of the article; a human has to open it, retrying will not help", Host: u.Hostname()}
		}
		// An empty shell is what a JavaScript application looks like to a reader that
		// does not run scripts. Retrying the same fetch cannot change that.
		if page.Kind == KindEmpty {
			return Page{}, &Failure{Stage: StageContent, Code: CodeNeedsRender, Message: "the page carries no readable text without running its scripts; this needs a rendering path", Host: u.Hostname()}
		}
		if len(page.Content) > 12<<10 {
			page.Truncated = true
			page.Content = bounded(page.Content, 12<<10)
			page.TextBytes = len(page.Content)
		}
		return page, nil
	}
	if kind != "text/plain" && kind != "application/json" {
		return Page{}, &Failure{Stage: StageContent, Code: CodeUnsupportedType, Message: "the address serves " + kind + ", which is not a readable page; a downloadable file needs the document path", Host: u.Hostname()}
	}
	text := collapseWhitespace(string(data))
	if len(text) > 12<<10 {
		truncated = true
		text = bounded(text, 12<<10)
	}
	return Page{URL: u.String(), Content: text, Truncated: truncated, Kind: KindArticle, Parse: "good", Bytes: len(data), TextBytes: len(text)}, nil
}

// singleCJK reports a field that is exactly one CJK ideograph. Models sometimes
// split one unknown term into characters ("大 肥 鱼"); searching that returns
// per-character dictionary pages instead of the term the user meant.
func singleCJK(field string) bool {
	runes := []rune(field)
	if len(runes) != 1 {
		return false
	}
	c := runes[0]
	return c >= 0x3400 && c <= 0x4DBF || c >= 0x4E00 && c <= 0x9FFF || c >= 0xF900 && c <= 0xFAFF
}

// NormalizeQuery collapses whitespace and rejoins a run of spaced single CJK
// characters, so "大 肥 鱼" and "大肥鱼" reach the upstream as the same term.
func NormalizeQuery(query string) string {
	fields := strings.Fields(query)
	out := make([]string, 0, len(fields))
	for i := 0; i < len(fields); i++ {
		if singleCJK(fields[i]) {
			j := i
			var merged strings.Builder
			for j < len(fields) && singleCJK(fields[j]) {
				merged.WriteString(fields[j])
				j++
			}
			if j-i > 1 {
				out = append(out, merged.String())
				i = j - 1
				continue
			}
		}
		out = append(out, fields[i])
	}
	return strings.Join(out, " ")
}

// SearchQuery validates and repairs a model-supplied query before it reaches
// the upstream, so a single-character lookup is refused instead of answered.
func SearchQuery(raw string) (string, error) {
	query := NormalizeQuery(raw)
	if query == "" {
		return "", errors.New("search query is empty")
	}
	if len(query) > 1024 {
		return "", errors.New("search query exceeds 1024 bytes")
	}
	if singleCJK(query) {
		return "", errors.New("search query is a single character; pass the whole term the user mentioned instead of one character at a time")
	}
	return query, nil
}

// hasCJK reports a term containing at least one CJK ideograph.
func hasCJK(term string) bool {
	for _, r := range term {
		if (r >= 0x3400 && r <= 0x4DBF) || (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0xF900 && r <= 0xFAFF) {
			return true
		}
	}
	return false
}

// coreTerm is the subject a query is really about: the first field long enough
// to be a name, preferring CJK because the RSS upstream degrades on those.
func coreTerm(query string) string {
	fields := strings.Fields(strings.NewReplacer(`"`, " ", `“`, " ", `”`, " ", `「`, " ", `」`, " ").Replace(NormalizeQuery(query)))
	first := ""
	for _, field := range fields {
		runes := 0
		for _, r := range field {
			if (r >= 0x3400 && r <= 0x4DBF) || (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0xF900 && r <= 0xFAFF) {
				runes++
			}
		}
		if first == "" {
			first = field
		}
		if runes >= 2 {
			return field
		}
	}
	return first
}

// matchesCore reports whether any result actually talks about the subject. The
// upstream happily returns per-character dictionary pages for a multi-term CJK
// query, and those never contain the term itself.
func matchesCore(results []Result, core string) bool {
	if core == "" {
		return true
	}
	for _, result := range results {
		if strings.Contains(result.Title, core) || strings.Contains(result.Snippet, core) {
			return true
		}
	}
	return false
}

func (c *Client) Search(ctx context.Context, query string) ([]Result, error) {
	if !c.SearchEnabled() {
		return nil, errors.New("web search is disabled")
	}
	query, err := SearchQuery(query)
	if err != nil {
		return nil, err
	}
	out, err := c.searchOnce(ctx, query)
	if err != nil {
		return nil, err
	}
	// Extra qualifiers make the RSS endpoint fall back to single-character
	// matches for CJK subjects. When the subject is missing, retry it alone.
	// Latin queries keep their qualifiers: the degradation is CJK-specific.
	core := coreTerm(query)
	if core != "" && core != query && hasCJK(core) && !matchesCore(out, core) {
		if retry, retryErr := c.searchOnce(ctx, core); retryErr == nil && matchesCore(retry, core) {
			return retry, nil
		}
	}
	return out, nil
}

// SearchSingle performs exactly one engine request. Background discovery uses
// this instead of the interactive search's optional query-refinement retry.
func (c *Client) SearchSingle(ctx context.Context, query string) ([]Result, error) {
	if !c.SearchEnabled() {
		return nil, errors.New("web search is disabled")
	}
	query, err := SearchQuery(query)
	if err != nil {
		return nil, err
	}
	return c.searchOnce(ctx, query)
}

// latinQuery reports whether a query is mostly Latin script, which is when asking
// an engine for its English index is meaningful.
func latinQuery(query string) bool {
	latin, other := 0, 0
	for _, r := range query {
		switch {
		case r < 0x250:
			if r > 0x7F || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				latin++
			}
		default:
			other++
		}
	}
	return latin > other
}

// searchOnce reads the engine's public results page and parses candidates from
// its HTML. No third-party JSON/RSS wrapper sits in between: what the engine
// renders is what the model sees.
func (c *Client) searchOnce(ctx context.Context, query string) ([]Result, error) {
	u, err := url.Parse(c.searchURL)
	if err != nil {
		return nil, failure(StagePolicy, CodePolicyDenied, "the configured search endpoint is not a valid URL")
	}
	values := u.Query()
	values.Set("q", query)
	values.Set("count", "10")
	// Bing serves unrelated popular pages to this host unless the English index is
	// requested explicitly: a query for "Landlock unprivileged access control"
	// returned Zhihu threads about phone repairs, with zero on-topic result blocks;
	// with ensearch=1 the same request yielded six. Only ask for it when the query
	// is Latin, since forcing the English index cannot help a Chinese query and
	// would only change what the engine pretends to have found.
	if strings.HasSuffix(strings.ToLower(u.Hostname()), "bing.com") && latinQuery(query) {
		values.Set("ensearch", "1")
	}
	u.RawQuery = values.Encode()
	data, kind, truncated, err := c.readAs(ctx, u, "", browserAgent)
	if err != nil {
		return nil, err
	}
	if kind != "text/html" {
		return nil, &Failure{Stage: StageContent, Code: CodeUnsupportedType, Message: "the search engine returned " + kind + " instead of a results page", Host: u.Hostname()}
	}
	if truncated {
		return nil, &Failure{Stage: StageContent, Code: CodeOutputTruncated, Message: "the results page exceeded the size limit and was cut, so its result blocks cannot be trusted", Host: u.Hostname()}
	}
	page := string(data)
	results := parseResults(page)
	if len(results) == 0 {
		// Three unrelated outcomes used to share one message. A person who hits a
		// wall, a query that honestly found nothing, and an adapter that no longer
		// matches the engine need different next steps.
		if challengePage("", page) {
			return nil, &Failure{Stage: StageParse, Code: CodeChallengeRequired, Message: "the search engine answered with a verification page; a human has to open it and retrying will not help", Host: u.Hostname()}
		}
		if noResultsPage(page) {
			return nil, &Failure{Stage: StageParse, Code: CodeNoResults, Message: "the search engine found nothing for this query; try different or broader words", Host: u.Hostname()}
		}
		return nil, &Failure{Stage: StageParse, Code: CodeSearchParseFailed, Message: "the results page no longer matches what this reader can parse, so the engine's markup has changed; this is a tooling problem, not an empty query", Host: u.Hostname()}
	}
	if len(results) > 5 {
		results = results[:5]
	}
	return results, nil
}
func (c *Client) Close() {
	if c != nil {
		c.http.CloseIdleConnections()
	}
}

// ResultURL only validates a returned link; fetching still requires public DNS
// checks and the independent fetch allowlist.
func ResultURL(value string) (string, error) {
	u, err := parseURL(value)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// Download reads the raw bytes of one public HTTPS address.
//
// It exists because the confined shell has no sockets: a task that needs a file
// from the web must get it through the same egress checks as every other request,
// not by opening a connection of its own. The address still has to pass the policy
// that guards a page read - public DNS, no private or reserved destination, no
// redirect - only the accepted content types and the size differ, because the
// caller asked for bytes rather than for something to read.
func (c *Client) Download(ctx context.Context, value string, maxBytes int64) ([]byte, string, error) {
	if c == nil || !c.FetchEnabled() {
		return nil, "", failure(StagePolicy, CodePolicyDenied, "web download is disabled on this host")
	}
	u, err := parseURL(value)
	if err != nil {
		return nil, "", err
	}
	if len(c.hosts) > 0 && !c.hosts[u.Hostname()] {
		return nil, "", &Failure{Stage: StagePolicy, Code: CodePolicyDenied, Message: "this host is not in the fetch allowlist", Host: u.Hostname()}
	}
	if maxBytes <= 0 || maxBytes > 64<<20 {
		maxBytes = 16 << 20
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", failure(StagePolicy, CodeInvalidURL, "the address is not a valid request target")
	}
	req.Header.Set("User-Agent", browserAgent)
	req.Header.Set("Accept", "*/*")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", context.Cause(ctx)
		}
		return nil, "", transportFailure(err, u.Hostname())
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", statusFailure(resp, u.Hostname(), time.Now())
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, "", &Failure{Stage: StageTransport, Code: CodeConnectTimeout, Message: "the connection broke while downloading", Retryable: true, Host: u.Hostname()}
	}
	if int64(len(data)) > maxBytes {
		return nil, "", &Failure{Stage: StageContent, Code: CodeOutputTruncated, Message: "the file is larger than the download limit", Host: u.Hostname()}
	}
	kind := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
	return data, kind, nil
}
