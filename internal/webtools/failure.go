package webtools

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// A web request can fail in several unrelated ways, and each one calls for a
// different next step: a blocked destination is final, a rate limit wants a wait,
// a parse failure wants a different query or source. Collapsing them into a single
// "web request failed" is why a failure used to tell the model nothing it could act
// on, and why the same mistake got retried unchanged.
type Failure struct {
	// Stage is where it broke: policy, transport, status, content or parse.
	Stage string `json:"stage"`
	// Code is the specific reason, stable enough for the model to branch on.
	Code    string `json:"code"`
	Message string `json:"message"`
	// Retryable says whether repeating the same request could ever work.
	Retryable bool `json:"retryable"`
	// RetryAfter is the server's own instruction, in seconds, when it gave one.
	RetryAfter int `json:"retry_after_seconds,omitempty"`
	Status     int `json:"http_status,omitempty"`
	Host       string `json:"host,omitempty"`
}

func (f *Failure) Error() string {
	message := f.Message
	if f.Status != 0 {
		message = fmt.Sprintf("%s (HTTP %d)", message, f.Status)
	}
	return fmt.Sprintf("%s [%s]", message, f.Code)
}

const (
	StagePolicy    = "policy"
	StageTransport = "transport"
	StageStatus    = "status"
	StageContent   = "content"
	StageParse     = "parse"
)

// The codes exist so a caller can decide what to do without parsing prose.
const (
	CodeDNSFailed          = "DNS_FAILED"
	CodePrivateDestination = "PRIVATE_DESTINATION"
	CodeConnectTimeout     = "CONNECT_TIMEOUT"
	CodeTLSFailed          = "TLS_FAILED"
	CodeRedirectBlocked    = "REDIRECT_BLOCKED"
	CodeAccessDenied       = "ACCESS_DENIED"
	CodeLoginRequired      = "LOGIN_REQUIRED"
	CodeChallengeRequired  = "CHALLENGE_REQUIRED"
	CodeRateLimited        = "RATE_LIMITED"
	CodeNotFound           = "NOT_FOUND"
	CodeServerError        = "SERVER_ERROR"
	CodeUnsupportedType    = "UNSUPPORTED_CONTENT_TYPE"
	CodeOutputTruncated    = "OUTPUT_TRUNCATED"
	CodeSearchParseFailed  = "SEARCH_PARSE_FAILED"
	CodeNoResults          = "NO_RESULTS"
	CodeNeedsRender        = "NEEDS_RENDER"
	CodePolicyDenied       = "POLICY_DENIED"
	CodeInvalidURL         = "INVALID_URL"
	CodeUnknown            = "UNKNOWN"
)

func failure(stage, code, message string) *Failure {
	return &Failure{Stage: stage, Code: code, Message: message}
}

// FailureOf extracts the structured reason from an error, so a caller can report
// it instead of a flattened string. Anything else becomes an opaque failure rather
// than being dropped.
func FailureOf(err error) *Failure {
	if err == nil {
		return nil
	}
	if failure, ok := err.(*Failure); ok {
		return failure
	}
	return &Failure{Stage: StageTransport, Code: "UNKNOWN", Message: err.Error()}
}

// retryAfterSeconds reads the server's wait instruction. A malformed or absurd
// value is ignored rather than turned into a long sleep.
func retryAfterSeconds(header http.Header, now time.Time) int {
	value := strings.TrimSpace(header.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 || seconds > 3600 {
			return 0
		}
		return seconds
	}
	if when, err := http.ParseTime(value); err == nil {
		seconds := int(when.Sub(now).Seconds())
		if seconds < 0 || seconds > 3600 {
			return 0
		}
		return seconds
	}
	return 0
}

// statusFailure turns a non-200 response into something actionable. A page that
// needs a login is not the same as one that does not exist, and neither is the
// same as being asked to slow down.
func statusFailure(resp *http.Response, host string, now time.Time) *Failure {
	out := &Failure{Stage: StageStatus, Status: resp.StatusCode, Host: host}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		out.Code, out.Message = CodeLoginRequired, "this page requires signing in, which this tool cannot do"
	case http.StatusForbidden:
		out.Code, out.Message = CodeAccessDenied, "the site refused the request; it may block automated clients"
	case http.StatusNotFound, http.StatusGone:
		out.Code, out.Message = CodeNotFound, "the page does not exist at this address"
	case http.StatusTooManyRequests:
		out.Code, out.Message = CodeRateLimited, "the site asked for fewer requests; do not retry in a loop"
		out.Retryable, out.RetryAfter = true, retryAfterSeconds(resp.Header, now)
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		out.Code, out.Message = CodeConnectTimeout, "the site did not answer in time"
		out.Retryable = true
	default:
		if resp.StatusCode >= 500 {
			out.Code, out.Message = CodeServerError, "the site reported its own failure"
			out.Retryable, out.RetryAfter = true, retryAfterSeconds(resp.Header, now)
			return out
		}
		out.Code, out.Message = CodeAccessDenied, "the site answered with an unexpected status"
	}
	return out
}

// challengePage reports a page that is asking a human to prove they are one. It is
// checked before extraction because such a page has text, and returning that text
// as the article would be a confident lie.
func challengePage(title, text string) bool {
	probe := strings.ToLower(title + " " + text)
	for _, marker := range []string{
		"captcha", "are you a robot", "verify you are human", "unusual traffic",
		"enable javascript and cookies to continue", "checking your browser",
		"人机验证", "验证码", "请完成安全验证", "访问过于频繁",
	} {
		if strings.Contains(probe, marker) {
			return true
		}
	}
	return false
}

// needsRender reports a shell that will never hold the article, however long it is
// read. The model should switch to a rendering path instead of retrying the fetch.
func needsRender(text string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(text))
	if len(trimmed) > 0 {
		return false
	}
	return true
}

// noResultsPage reports a results page that honestly found nothing. It is a
// different outcome from a page this reader no longer understands: one means the
// query is empty, the other means the adapter is stale, and the model should react
// differently to each.
func noResultsPage(text string) bool {
	probe := strings.ToLower(text)
	for _, marker := range []string{
		"there are no results", "no results for", "did not match any documents",
		"没有与此相关的结果", "未找到相关结果", "没有找到", "没有结果",
	} {
		if strings.Contains(probe, marker) {
			return true
		}
	}
	return false
}
