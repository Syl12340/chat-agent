package probe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"
)

// ModelRequest is a prepared step snapshot. No credentials or execution
// callbacks cross this seam. Providers must not mutate or retry the request.
type ModelRequest struct {
	Model           string
	System          string
	Messages        []Message
	Tools           []ToolSchema
	MaxOutputTokens int
	TokenParameter  string
}

type ModelProvider interface {
	Stream(context.Context, ModelRequest, func(Event)) (Message, error)
}

// ChatCompletionsProvider preserves the existing bounded SSE protocol.
type ChatCompletionsProvider struct {
	endpoint string
	key      string
	client   *http.Client
}

func (a *Agent) stream(ctx context.Context, assembly PromptAssembly, emit func(Event)) (Message, error) {
	if a.config.Demo {
		return a.demoStream(ctx, assembly.Tools, emit)
	}
	return a.provider.Stream(ctx, ModelRequest{Model: a.config.Model, System: assembly.System,
		Messages: cloneMessages(assembly.Messages), Tools: assembly.Tools.Schemas(),
		MaxOutputTokens: a.config.MaxOutputTokens, TokenParameter: a.config.TokenParameter}, emit)
}

func cloneMessages(messages []Message) []Message {
	out := append([]Message(nil), messages...)
	for i := range out {
		out[i].ToolCalls = append([]ToolCall(nil), out[i].ToolCalls...)
	}
	return out
}

func (a *Agent) chat(ctx context.Context, messages []Message, system string, withTools bool, emit func(Event)) (Message, error) {
	return a.chatAs(ctx, a.config.Model, messages, system, withTools, emit)
}

// Utility generation is deliberately tool-free, with no hidden fallback route.
func (a *Agent) chatAs(ctx context.Context, model string, messages []Message, system string, withTools bool, emit func(Event)) (Message, error) {
	if withTools {
		return Message{}, errors.New("tool requests require step assembly")
	}
	return a.provider.Stream(ctx, ModelRequest{Model: model, System: system, Messages: cloneMessages(messages),
		MaxOutputTokens: a.config.MaxOutputTokens, TokenParameter: a.config.TokenParameter}, emit)
}

func (p *ChatCompletionsProvider) Stream(ctx context.Context, input ModelRequest, emit func(Event)) (Message, error) {
	model := input.Model
	system := input.System
	messages := input.Messages
	if strings.TrimSpace(model) == "" {
		return Message{}, errors.New("model name is required")
	}
	messages = append([]Message{{Role: "system", Content: system}}, messages...)
	body := map[string]any{
		"model": model, "messages": messages, "stream": true,
		input.TokenParameter: input.MaxOutputTokens,
	}
	if len(input.Tools) > 0 {
		tools := make([]any, 0, len(input.Tools))
		for _, schema := range input.Tools {
			tools = append(tools, map[string]any{"type": "function", "function": schema})
		}
		body["tools"] = tools
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Message{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(payload))
	if err != nil {
		return Message{}, errors.New("cannot construct model request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+p.key)
	resp, err := p.client.Do(req)
	if err != nil {
		return Message{}, fmt.Errorf("model request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Omit raw response bodies and credentials from errors.
		return Message{}, fmt.Errorf("model endpoint returned HTTP %d", resp.StatusCode)
	}
	media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if media != "text/event-stream" {
		return Message{}, errors.New("model endpoint did not return text/event-stream")
	}
	limited := &io.LimitedReader{R: resp.Body, N: (4 << 20) + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var text strings.Builder
	pending := map[int]*ToolCall{}
	finish := ""
	terminated := false
	var data []string
	consume := func() (bool, error) {
		if len(data) == 0 {
			return false, nil
		}
		line := strings.Join(data, "\n")
		data = nil
		if line == "[DONE]" {
			return true, nil
		}
		var chunk struct {
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				Index  int    `json:"index"`
				Finish string `json:"finish_reason"`
				Delta  struct {
					Content string `json:"content"`
					Calls   []struct {
						Index    int          `json:"index"`
						ID       string       `json:"id"`
						Type     string       `json:"type"`
						Function FunctionCall `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(line), &chunk) != nil {
			return false, errors.New("invalid SSE JSON")
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return false, errors.New("model stream returned an error")
		}
		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				return false, errors.New("multiple model choices are unsupported")
			}
			if finish != "" && (choice.Delta.Content != "" || len(choice.Delta.Calls) > 0) {
				return false, errors.New("model data after finish reason")
			}
			if choice.Delta.Content != "" {
				text.WriteString(choice.Delta.Content)
				emit(Event{Type: EventTextDelta, Text: choice.Delta.Content})
			}
			for _, part := range choice.Delta.Calls {
				if part.Index < 0 || part.Index >= 64 {
					return false, errors.New("too many tool calls")
				}
				call := pending[part.Index]
				if call == nil {
					call = &ToolCall{}
					pending[part.Index] = call
				}
				if part.ID != "" {
					call.ID += part.ID
				}
				if part.Type != "" {
					call.Type = part.Type
				}
				call.Function.Name += part.Function.Name
				call.Function.Arguments += part.Function.Arguments
			}
			if choice.Finish != "" {
				finish = choice.Finish
			}
		}
		return false, nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			done, err := consume()
			if err != nil {
				return Message{}, err
			}
			if done {
				terminated = true
				break
			}
		} else if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(line, "data:")
			data = append(data, strings.TrimPrefix(value, " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return Message{}, fmt.Errorf("read model stream: %w", err)
	}
	if ctx.Err() != nil {
		return Message{}, context.Cause(ctx)
	}
	if len(data) > 0 {
		done, err := consume()
		if err != nil {
			return Message{}, err
		}
		terminated = done
	}
	if limited.N == 0 {
		return Message{}, errors.New("model stream exceeds 4 MiB limit")
	}
	if !terminated {
		return Message{}, errors.New("model stream ended without [DONE]")
	}
	response := Message{Role: "assistant", Content: text.String()}
	if finish == "tool_calls" && len(pending) > 0 {
		indices := make([]int, 0, len(pending))
		for index := range pending {
			indices = append(indices, index)
		}
		sort.Ints(indices)
		ids := map[string]bool{}
		for _, index := range indices {
			call := *pending[index]
			if call.ID == "" || ids[call.ID] || call.Type != "function" || call.Function.Name == "" || !json.Valid([]byte(call.Function.Arguments)) {
				return Message{}, errors.New("incomplete or invalid tool call")
			}
			ids[call.ID] = true
			response.ToolCalls = append(response.ToolCalls, call)
		}
		return response, nil
	}
	if finish != "stop" || len(pending) > 0 || strings.TrimSpace(response.Content) == "" {
		return Message{}, fmt.Errorf("incomplete model response (finish reason %q)", finish)
	}
	return response, nil
}
