package probe

import (
	"context"
	"errors"
	"strings"
)

// Generate is a bounded, tool-free, one-request generation for a consented
// check-in. The caller reserves budget before calling; there are no retries.
func Generate(ctx context.Context, c Config, system, input string) (string, error) {
	if strings.TrimSpace(input) == "" || len(input) > 32<<10 {
		return "", errors.New("invalid generation reference")
	}
	if c.Demo {
		return "这是一条本地演示问候。今天想从哪件小事聊起？", nil
	}
	a, err := New(c, NewSession())
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	response, err := a.chat(ctx, []Message{{Role: "user", Content: input}}, system, false, func(Event) {})
	if err != nil {
		return "", err
	}
	if len(response.ToolCalls) > 0 || strings.TrimSpace(response.Content) == "" || len(response.Content) > 8192 {
		return "", errors.New("invalid check-in response")
	}
	return response.Content, nil
}
