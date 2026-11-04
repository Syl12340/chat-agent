package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestRepeatedToolBatchFinalizes(t *testing.T) {
	var requests, executions atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []any `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		n := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n <= 2 {
			if len(body.Tools) == 0 {
				t.Error("tools unexpectedly unavailable")
			}
			chunk(w, map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("call-%d", n), "type": "function", "function": map[string]string{"name": "fixture_action", "arguments": "{}"}}}}, "tool_calls")
			fmt.Fprint(w, "data: [DONE]\n\n")
		} else {
			if len(body.Tools) != 0 {
				t.Error("repeated action did not force a final response")
			}
			answer(w, "已经处理，可以继续聊。")
		}
	}))
	defer srv.Close()
	c := DefaultConfig()
	c.BaseURL, c.APIKey, c.Model = srv.URL, "fixture-key", "fixture-model"
	c.Tools = []Tool{{Name: "fixture_action", Description: "Fixture persistent action.", Parameters: json.RawMessage(`{"type":"object"}`), Execute: func(context.Context, json.RawMessage) (ToolResult, error) {
		executions.Add(1)
		return ToolResult{Output: `{"saved":true}`}, nil
	}}}
	a, err := New(c, NewSession())
	if err != nil {
		t.Fatal(err)
	}
	if err = Run(context.Background(), a, c.Timeout, "帮我处理这件事", nil); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 3 || executions.Load() != 1 {
		t.Fatalf("requests=%d executions=%d", requests.Load(), executions.Load())
	}
}
