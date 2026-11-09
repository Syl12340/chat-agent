package probe

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"chat-agent/internal/media"
	"sort"
	"strings"
)

// ModelRequest is a prepared step snapshot. No credentials or execution
// callbacks cross this seam. Providers must not mutate or retry the request.
type ModelRequest struct {
	Model           string       `json:"model"`
	System          string       `json:"system"`
	Messages        []Message    `json:"messages"`
	Tools           []ToolSchema `json:"tools,omitempty"`
	MaxOutputTokens int          `json:"max_output_tokens"`
	TokenParameter  string       `json:"token_parameter"`
	Thinking        string       `json:"thinking,omitempty"`
	ReasoningEffort string       `json:"reasoning_effort,omitempty"`
	HistoryFloor    int          `json:"history_floor"`
	HostStep        StepContext  `json:"host_step"`
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

func (a *Agent) stream(ctx context.Context, step int, assembly PromptAssembly, emit func(Event)) (Message, error) {
	if a.config.Demo && a.config.Recorder == nil {
		return a.demoStream(ctx, assembly.Tools, emit)
	}
	messages := cloneMessages(assembly.Messages)
	remaining := 3
	for i := len(messages) - 1; i >= 0; i-- {
		images := messages[i].Images
		if len(images) > remaining {
			messages[i].Images = images[len(images)-remaining:]
			messages[i].Content += "\n[Some earlier images are not included in this request. Use existing visible descriptions only; ask the user to resend an earlier image if it must be viewed again.]"
		}
		remaining -= len(messages[i].Images)
	}
	for i := range messages {
		for j := range messages[i].Images {
			if a.config.ResolveImage == nil {
				return Message{}, errors.New("image loading is unavailable")
			}
			url, err := a.config.ResolveImage(ctx, messages[i].Images[j])
			if err != nil {
				return Message{}, errors.New("image attachment is unavailable")
			}
			messages[i].Images[j].DataURL = url
			if messages[i].Images[j].SHA256 == "" {
				comma := strings.IndexByte(url, ',')
				if comma < 0 {
					return Message{}, errors.New("image attachment is unavailable")
				}
				data, decodeErr := base64.StdEncoding.DecodeString(url[comma+1:])
				if decodeErr != nil {
					return Message{}, errors.New("image attachment is unavailable")
				}
				digest := sha256.Sum256(data)
				messages[i].Images[j].SHA256 = hex.EncodeToString(digest[:])
			}
		}
	}
	request := ModelRequest{Model: a.config.Model, System: assembly.System,
		Messages: modelVisibleMessages(messages), Tools: assembly.Tools.Schemas(), Thinking: a.config.Thinking, ReasoningEffort: a.config.ReasoningEffort,
		MaxOutputTokens: a.config.MaxOutputTokens, TokenParameter: a.config.TokenParameter, HistoryFloor: assembly.HistoryFloor, HostStep: assembly.Step}
	if a.config.Recorder != nil {
		if err := a.config.Recorder.ModelRequested(ctx, step, request); err != nil {
			return Message{}, recordingError("model request", err)
		}
	}
	var response Message
	var runErr error
	recordingEmit := emit
	var partialText, partialReasoning strings.Builder
	if a.config.Recorder != nil {
		recordingEmit = func(event Event) {
			if event.Type == EventTextDelta {
				partialText.WriteString(event.Text)
			} else if event.Type == EventReasoningDelta {
				partialReasoning.WriteString(event.Text)
			}
			emit(event)
		}
	}
	if a.config.Demo {
		response, runErr = a.demoStream(ctx, assembly.Tools, recordingEmit)
	} else {
		response, runErr = a.provider.Stream(ctx, request, recordingEmit)
	}
	if a.config.Recorder != nil {
		recorded := response
		if runErr != nil {
			recorded = Message{Role: "assistant", Content: partialText.String(), ReasoningContent: partialReasoning.String()}
		}
		if err := a.config.Recorder.ModelFinished(context.WithoutCancel(ctx), step, recorded, runErr); err != nil {
			runErr = errors.Join(runErr, recordingError("model finish", err))
		}
	}
	return response, runErr
}

func cloneMessages(messages []Message) []Message {
	out := append([]Message(nil), messages...)
	for i := range out {
		out[i].ToolCalls = append([]ToolCall(nil), out[i].ToolCalls...)
		out[i].Images = append([]media.Image(nil), out[i].Images...)
	}
	return out
}

// modelVisibleMessages is the single projection used for both context sizing
// and provider requests. Final-answer reasoning is display/history data only;
// assistant reasoning attached to a tool call remains protocol state required
// by reasoning-capable providers on the following step.
func modelVisibleMessages(messages []Message) []Message {
	out := cloneMessages(messages)
	for i := range out {
		if len(out[i].ToolCalls) == 0 {
			out[i].ReasoningContent = ""
		}
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
	budget := a.config.MaxOutputTokens
	if budget == 0 {
		budget = 1024 // Utility summaries keep their existing bounded background cost.
	}
	return a.provider.Stream(ctx, ModelRequest{Model: model, System: system, Messages: modelVisibleMessages(messages),
		MaxOutputTokens: budget, TokenParameter: a.config.TokenParameter}, emit)
}

func (p *ChatCompletionsProvider) Stream(ctx context.Context, input ModelRequest, emit func(Event)) (Message, error) {
	model := input.Model
	system := input.System
	messages := modelVisibleMessages(input.Messages)
	if strings.TrimSpace(model) == "" {
		return Message{}, errors.New("model name is required")
	}
	wireMessages := make([]map[string]any, 0, len(messages)+1)
	wireMessages = append(wireMessages, map[string]any{"role": "system", "content": system})
	for _, message := range messages {
		var content any = message.Content
		if len(message.Images) > 0 {
			parts := []any{}
			if message.Content != "" {
				parts = append(parts, map[string]any{"type": "text", "text": message.Content})
			}
			for _, image := range message.Images {
				if !strings.HasPrefix(image.DataURL, "data:"+image.MIMEType+";base64,") || len(image.DataURL) > 6<<20 {
					return Message{}, errors.New("image attachment was not prepared")
				}
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": image.DataURL}})
			}
			content = parts
		}
		wire := map[string]any{"role": message.Role, "content": content}
		if len(message.ToolCalls) > 0 {
			wire["tool_calls"] = message.ToolCalls
		}
		if message.ToolCallID != "" {
			wire["tool_call_id"] = message.ToolCallID
		}
		if len(message.ToolCalls) > 0 && message.ReasoningContent != "" {
			wire["reasoning_content"] = message.ReasoningContent
		}
		wireMessages = append(wireMessages, wire)
	}
	body := map[string]any{
		"model": model, "messages": wireMessages, "stream": true,
	}
	// Zero omits the parameter entirely; it must not become a zero-token request.
	if input.MaxOutputTokens > 0 {
		body[input.TokenParameter] = input.MaxOutputTokens
	}
	if input.Thinking != "" {
		body["thinking"] = map[string]string{"type": input.Thinking}
	}
	if input.ReasoningEffort != "" {
		body["reasoning_effort"] = input.ReasoningEffort
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
		return Message{}, &ModelError{Kind: "transport", cause: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Omit raw response bodies and credentials from errors.
		return Message{}, &ModelError{Kind: "http", HTTPStatus: resp.StatusCode}
	}
	media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if media != "text/event-stream" {
		return Message{}, &ModelError{Kind: "stream"}
	}
	limited := &io.LimitedReader{R: resp.Body, N: (4 << 20) + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var text strings.Builder
	var reasoning strings.Builder
	thinkingSent := false
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
					Content   string `json:"content"`
					Reasoning string `json:"reasoning_content"`
					Calls     []struct {
						Index    int          `json:"index"`
						ID       string       `json:"id"`
						Type     string       `json:"type"`
						Function FunctionCall `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(line), &chunk) != nil {
			return false, &ModelError{Kind: "stream"}
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return false, &ModelError{Kind: "stream"}
		}
		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				return false, errors.New("multiple model choices are unsupported")
			}
			if finish != "" && (choice.Delta.Content != "" || choice.Delta.Reasoning != "" || len(choice.Delta.Calls) > 0) {
				return false, errors.New("model data after finish reason")
			}
			if choice.Delta.Reasoning != "" {
				if reasoning.Len()+len(choice.Delta.Reasoning) > 256<<10 {
					return false, errors.New("reasoning exceeds limit")
				}
				reasoning.WriteString(choice.Delta.Reasoning)
				if !thinkingSent {
					emit(Event{Type: EventThinking})
					thinkingSent = true
				}
				emit(Event{Type: EventReasoningDelta, Text: choice.Delta.Reasoning})
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
		return Message{}, &ModelError{Kind: "transport", cause: err}
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
		return Message{}, &ModelError{Kind: "stream"}
	}
	response := Message{Role: "assistant", Content: text.String(), ReasoningContent: reasoning.String()}
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
	if finish == "length" {
		return Message{}, &ModelError{Kind: "output_limit"}
	}
	if finish == "stop" && len(pending) == 0 && strings.TrimSpace(response.Content) == "" {
		return Message{}, &ModelError{Kind: "empty"}
	}
	if finish != "stop" || len(pending) > 0 {
		return Message{}, fmt.Errorf("incomplete model response (finish reason %q)", finish)
	}
	return response, nil
}
