package probe

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestModelFailureBoundary(t *testing.T) {
	for _, fixture := range []struct {
		status       int
		stream, kind string
		transport    error
	}{
		{status: 429, stream: `{"error":"private-provider-body"}`, kind: "http"},
		{status: 200, stream: "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"private-reasoning\"},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n", kind: "output_limit"},
		{status: 200, stream: "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", kind: "empty"},
		{status: 200, stream: "data: {broken}\n\n", kind: "stream"},
		{kind: "transport", transport: context.DeadlineExceeded},
	} {
		calls := 0
		provider := &ChatCompletionsProvider{endpoint: "https://fixture.example/v1/chat/completions", key: "private-key", client: &http.Client{Transport: multimodalTransport(func(*http.Request) (*http.Response, error) {
			calls++
			if fixture.transport != nil {
				return nil, fixture.transport
			}
			return &http.Response{StatusCode: fixture.status, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(fixture.stream))}, nil
		})}}
		message, err := provider.Stream(context.Background(), ModelRequest{Model: "fixture", TokenParameter: "max_tokens", MaxOutputTokens: 32}, func(ev Event) {
			if ev.Type == EventThinking && ev.Text != "" {
				t.Fatal("reasoning leaked")
			}
		})
		var failure *ModelError
		if !errors.As(err, &failure) || failure.Kind != fixture.kind || message.Content != "" || calls != 1 {
			t.Fatalf("unexpected failure: %v", err)
		}
		if strings.Contains(err.Error(), "private") {
			t.Fatal("raw provider data leaked")
		}
		if fixture.transport != nil && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("timeout cause lost")
		}
	}
}
