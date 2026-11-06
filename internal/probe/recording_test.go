package probe_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pocket-agent/internal/probe"
	"pocket-agent/internal/runlog"
	"pocket-agent/internal/tasks"
)

type toolProvider struct{ calls int }

func (p *toolProvider) Stream(_ context.Context, _ probe.ModelRequest, _ func(probe.Event)) (probe.Message, error) {
	p.calls++
	if p.calls == 1 {
		return probe.Message{Role: "assistant", ToolCalls: []probe.ToolCall{{ID: "provider-call", Type: "function", Function: probe.FunctionCall{Name: "todo_add", Arguments: `{"title":"write once"}`}}}}, nil
	}
	return probe.Message{Role: "assistant", Content: "done"}, nil
}

type failedSettlement struct {
	inner  probe.RunRecorder
	failed bool
}

func (r *failedSettlement) AttemptStarted(ctx context.Context, value probe.AttemptRecord) error {
	return r.inner.AttemptStarted(ctx, value)
}
func (r *failedSettlement) ModelRequested(ctx context.Context, step int, value probe.ModelRequest) error {
	return r.inner.ModelRequested(ctx, step, value)
}
func (r *failedSettlement) ModelFinished(ctx context.Context, step int, value probe.Message, runErr error) error {
	return r.inner.ModelFinished(ctx, step, value, runErr)
}
func (r *failedSettlement) ToolStarted(ctx context.Context, step int, call probe.ToolCall, arguments json.RawMessage, receipt probe.ReceiptCapability) (string, error) {
	return r.inner.ToolStarted(ctx, step, call, arguments, receipt)
}
func (r *failedSettlement) ToolFinished(context.Context, int, probe.ToolCall, string, probe.ToolResult) error {
	r.failed = true
	return errors.New("injected result commit failure")
}
func (r *failedSettlement) AttemptFinished(ctx context.Context, value probe.AttemptFinish) error {
	if r.failed {
		return errors.New("writer already closed")
	}
	return r.inner.AttemptFinished(ctx, value)
}

func TestBusinessReceiptSurvivesRunSettlementFailure(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	taskRoot := filepath.Join(t.TempDir(), "tasks")
	store, err := tasks.Open(taskRoot)
	if err != nil {
		t.Fatal(err)
	}
	scope := runlog.Scope{SessionID: "session-one", RoleID: "companion", RequestID: "request-one"}
	durable, err := runlog.Open(runtimeRoot, scope)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &failedSettlement{inner: durable}
	provider := &toolProvider{}
	operationID := ""
	argumentsDigest := ""
	c := probe.DefaultConfig()
	c.Model, c.Provider = "fixture", provider
	c.RoleID, c.SessionID, c.RequestID = scope.RoleID, scope.SessionID, scope.RequestID
	c.Recorder = recorder
	c.Tools = []probe.Tool{{
		Name: "todo_add", Receipt: probe.ReceiptAtomic, Description: "add",
		Parameters: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, input json.RawMessage) (probe.ToolResult, error) {
			operationID = probe.ToolOperationID(ctx)
			digest := sha256.Sum256(input)
			argumentsDigest = hex.EncodeToString(digest[:])
			item, receipt, _, err := store.AddWithReceipt(scope.RoleID, "todo", "write once", operationID, argumentsDigest, "todo_add", "agent", scope.SessionID, nil, time.Now())
			if err != nil {
				return probe.ToolResult{}, err
			}
			output, _ := json.Marshal(map[string]any{"item": item, "receipt": receipt})
			return probe.ToolResult{Output: string(output)}, nil
		},
	}}
	session := probe.NewSession()
	agent, err := probe.New(c, session)
	if err != nil {
		t.Fatal(err)
	}
	err = probe.Run(context.Background(), agent, time.Second, "create it", nil)
	if !errors.Is(err, probe.ErrRecording) || provider.calls != 1 || operationID == "" {
		t.Fatalf("run err=%v calls=%d operation=%q", err, provider.calls, operationID)
	}
	if got := len(session.Messages()); got != 1 {
		t.Fatalf("partial tool batch entered history: messages=%d", got)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if err = durable.Close(); err == nil {
		t.Fatal("failed settlement recorder closed as a complete attempt")
	}

	store, err = tasks.Open(taskRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	receipt, ok, err := store.Receipt(scope.RoleID, operationID)
	if err != nil || !ok || receipt.ArgumentsSHA256 != argumentsDigest {
		t.Fatalf("receipt=%#v ok=%v err=%v", receipt, ok, err)
	}
	_, _, replayed, err := store.AddWithReceipt(scope.RoleID, "todo", "write once", operationID, argumentsDigest, "todo_add", "agent", scope.SessionID, nil, time.Now())
	items, listErr := store.List(scope.RoleID, "todo")
	if err != nil || listErr != nil || !replayed || len(items) != 1 {
		t.Fatalf("replay err=%v listErr=%v replayed=%v items=%d", err, listErr, replayed, len(items))
	}
	events, err := runlog.ReadEvents(runtimeRoot, scope.SessionID)
	if err != nil || len(events) != 4 || events[len(events)-1].Type != "tool.started" || events[len(events)-1].OperationID != operationID {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	snapshot, err := os.ReadFile(filepath.Join(runtimeRoot, scope.SessionID, "g-1", filepath.FromSlash(events[1].Reference.Path)))
	if err != nil || !strings.Contains(string(snapshot), `"source":"runtime-context"`) || !strings.Contains(string(snapshot), `"host_step"`) {
		t.Fatalf("prepared request metadata missing: err=%v", err)
	}

	// A tool without a business receipt still runs at most once. Its durable
	// start remains the only evidence, so recovery must classify it as unknown.
	noneScope := runlog.Scope{SessionID: "session-none", RoleID: "companion", RequestID: "request-none"}
	noneDurable, err := runlog.Open(runtimeRoot, noneScope)
	if err != nil {
		t.Fatal(err)
	}
	noneProvider := &toolProvider{}
	noneRecorder := &failedSettlement{inner: noneDurable}
	noneCalls := 0
	noneConfig := probe.DefaultConfig()
	noneConfig.Model, noneConfig.Provider = "fixture", noneProvider
	noneConfig.RoleID, noneConfig.SessionID, noneConfig.RequestID, noneConfig.Recorder = noneScope.RoleID, noneScope.SessionID, noneScope.RequestID, noneRecorder
	noneConfig.Tools = []probe.Tool{{Name: "todo_add", Receipt: probe.ReceiptNone, Description: "fixture", Parameters: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}`), Execute: func(context.Context, json.RawMessage) (probe.ToolResult, error) {
		noneCalls++
		return probe.ToolResult{Output: `{"ok":true}`}, nil
	}}}
	noneAgent, err := probe.New(noneConfig, probe.NewSession())
	if err != nil {
		t.Fatal(err)
	}
	if err = probe.Run(context.Background(), noneAgent, time.Second, "run once", nil); !errors.Is(err, probe.ErrRecording) || noneCalls != 1 || noneProvider.calls != 1 {
		t.Fatalf("none receipt run err=%v tools=%d model=%d", err, noneCalls, noneProvider.calls)
	}
	if err = noneDurable.Close(); err == nil {
		t.Fatal("none receipt recorder closed as a complete attempt")
	}
	noneEvents, err := runlog.ReadEvents(runtimeRoot, noneScope.SessionID)
	if err != nil || len(noneEvents) != 4 || noneEvents[3].Type != "tool.started" {
		t.Fatalf("none receipt events=%#v err=%v", noneEvents, err)
	}
	noneStart, err := os.ReadFile(filepath.Join(runtimeRoot, noneScope.SessionID, "g-1", filepath.FromSlash(noneEvents[3].Reference.Path)))
	if err != nil || !strings.Contains(string(noneStart), `"receipt_capability":"none"`) {
		t.Fatalf("none receipt capability missing: err=%v", err)
	}
}

type canceledProvider struct{ calls int }

func (p *canceledProvider) Stream(_ context.Context, _ probe.ModelRequest, emit func(probe.Event)) (probe.Message, error) {
	p.calls++
	emit(probe.Event{Type: probe.EventTextDelta, Text: "partial only"})
	return probe.Message{}, context.Canceled
}

func TestInterruptedStreamPersistsPartialWithoutHistoryOrReplay(t *testing.T) {
	root := t.TempDir()
	scope := runlog.Scope{SessionID: "session-two", RoleID: "companion", RequestID: "request-two"}
	recorder, err := runlog.Open(root, scope)
	if err != nil {
		t.Fatal(err)
	}
	provider := &canceledProvider{}
	c := probe.DefaultConfig()
	c.Model, c.Provider = "fixture", provider
	c.RoleID, c.SessionID, c.RequestID, c.Recorder = scope.RoleID, scope.SessionID, scope.RequestID, recorder
	session := probe.NewSession()
	agent, err := probe.New(c, session)
	if err != nil {
		t.Fatal(err)
	}
	if err = probe.Run(context.Background(), agent, time.Second, "hello", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("run error=%v", err)
	}
	if provider.calls != 1 || len(session.Messages()) != 1 {
		t.Fatalf("calls=%d messages=%d", provider.calls, len(session.Messages()))
	}
	if err = recorder.Close(); err != nil {
		t.Fatal(err)
	}
	events, err := runlog.ReadEvents(root, scope.SessionID)
	if err != nil || len(events) != 4 || events[2].Type != "model.finished" || events[3].Type != "attempt.finished" {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	data, err := os.ReadFile(filepath.Join(root, scope.SessionID, "g-1", filepath.FromSlash(events[2].Reference.Path)))
	if err != nil || !strings.Contains(string(data), "partial only") || !strings.Contains(string(data), `"status":"canceled"`) {
		t.Fatalf("partial payload err=%v data=%s", err, data)
	}
}
