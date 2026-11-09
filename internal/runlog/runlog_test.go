package runlog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"chat-agent/internal/media"
	"chat-agent/internal/probe"
)

func TestRecorderPersistsPreparedRequestAndLifecycle(t *testing.T) {
	root := t.TempDir()
	scope := Scope{SessionID: "session-one", RoleID: "companion", RequestID: "request-one"}
	recorder, err := Open(root, scope)
	if err != nil {
		t.Fatal(err)
	}
	if second, secondErr := Open(root, scope); secondErr == nil {
		_ = second.Close()
		t.Fatal("second writer opened for the same session")
	}
	if err = recorder.AttemptStarted(context.Background(), probe.AttemptRecord{SessionID: scope.SessionID, RoleID: scope.RoleID, RequestID: scope.RequestID, Inputs: []probe.Message{{Role: "user", Content: "hello"}}, MaxTurns: 4, MaxTools: 4}); err != nil {
		t.Fatal(err)
	}
	request := probe.ModelRequest{
		Model:  "fixture",
		System: "system",
		Messages: []probe.Message{{
			Role: "user", Content: "hello",
			Images: []media.Image{{ID: "image-one", MIMEType: "image/png", Size: 3, Width: 1, Height: 1, SHA256: strings.Repeat("a", 64), DataURL: "data:image/png;base64,secret"}},
		}},
		Tools:           []probe.ToolSchema{{Name: "todo_add", Description: "add", Parameters: json.RawMessage(`{"type":"object"}`)}},
		TokenParameter:  "max_tokens",
		Thinking:        "enabled",
		ReasoningEffort: "high",
	}
	if err = recorder.ModelRequested(context.Background(), 0, request); err != nil {
		t.Fatal(err)
	}
	if err = recorder.ModelFinished(context.Background(), 0, probe.Message{Role: "assistant", ToolCalls: []probe.ToolCall{{ID: "provider-call", Type: "function", Function: probe.FunctionCall{Name: "todo_add", Arguments: `{"title":"x"}`}}}}, nil); err != nil {
		t.Fatal(err)
	}
	call := probe.ToolCall{ID: "provider-call", Type: "function", Function: probe.FunctionCall{Name: "todo_add", Arguments: `{"title":"x"}`}}
	operationID, err := recorder.ToolStarted(context.Background(), 0, call, json.RawMessage(`{"title":"x"}`), probe.ReceiptAtomic)
	if err != nil || operationID == "" {
		t.Fatalf("tool start: operation=%q err=%v", operationID, err)
	}
	if err = recorder.ToolFinished(context.Background(), 0, call, operationID, probe.ToolResult{Output: `{"ok":true}`, Receipt: &probe.BusinessReceipt{Store: "tasks", RoleID: scope.RoleID, OperationID: operationID, ArgumentsSHA256: strings.Repeat("a", 64), EntityIDs: []string{"task-one"}, ResultingRevisions: []int{1}, OutcomeDigest: strings.Repeat("b", 64)}}); err != nil {
		t.Fatal(err)
	}
	if err = recorder.AttemptFinished(context.Background(), probe.AttemptFinish{Status: "completed", StopReason: "final_response"}); err != nil {
		t.Fatal(err)
	}
	if err = recorder.Close(); err != nil {
		t.Fatal(err)
	}

	events, err := ReadEvents(root, scope.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"attempt.started", "model.requested", "model.finished", "tool.started", "tool.finished", "attempt.finished"}
	if len(events) != len(want) {
		t.Fatalf("events=%d want=%d", len(events), len(want))
	}
	for i := range want {
		if events[i].Type != want[i] || events[i].EventSeq != uint64(i+1) {
			t.Fatalf("event %d = %#v", i, events[i])
		}
	}
	if !strings.Contains(string(events[4].Data), `"settlement":"committed"`) {
		t.Fatalf("tool settlement=%s", events[4].Data)
	}
	ref := events[1].Reference
	if ref == nil || ref.Kind != "model_request" {
		t.Fatalf("missing model snapshot: %#v", ref)
	}
	data, err := os.ReadFile(filepath.Join(root, scope.SessionID, "g-1", filepath.FromSlash(ref.Path)))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if ref.SHA256 != hex.EncodeToString(digest[:]) || strings.Contains(string(data), "base64,secret") || !strings.Contains(string(data), strings.Repeat("a", 64)) || !strings.Contains(string(data), `"max_output_tokens":0`) {
		t.Fatal("snapshot hash, image reference, or omitted output-limit semantics were not preserved")
	}
}
