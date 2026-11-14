package topic

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"chat-agent/internal/pluginapi"
	"chat-agent/internal/probe"
	"chat-agent/internal/topics"
)

type fakeStore struct {
	mu       sync.Mutex
	settings topics.Settings
	writes   int
}

func (f *fakeStore) Settings(string) (topics.Settings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.settings, nil
}

func (f *fakeStore) UpdateSettings(_ string, p topics.Settings) (topics.Settings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p.Revision++
	f.settings = p
	f.writes++
	return p, nil
}

type fakeSource struct{ enabled bool }

func (f fakeSource) SearchEnabled() bool { return f.enabled }
func (f fakeSource) FetchEnabled() bool  { return f.enabled }

type fakeGate struct {
	err   error
	calls int
}

func (g *fakeGate) WithRoleWrite(_, _ string, write func(pluginapi.WriteScope) error) error {
	g.calls++
	if g.err != nil {
		return g.err
	}
	return write(pluginapi.WriteScope{})
}

const optIn = "你可以自己去看看我感兴趣的东西，我不介意。"

func newTool(t *testing.T, store Store, source Source, gate pluginapi.WriteGate) pluginapi.Tool {
	t.Helper()
	loader := pluginapi.NewLoader()
	loader.Add(New(store, source, gate))
	catalog, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	loaded, ok := catalog.Tool("topic_preferences")
	if !ok {
		t.Fatal("topic_preferences did not load")
	}
	return loaded.Tool
}

func turnContext(text string) context.Context {
	ctx := probe.ToolContext(context.Background(), "session-1", "companion", "call-1", "")
	return probe.WithToolInputs(ctx, []probe.Message{{Role: "user", Content: text}})
}

func TestEnablingNeedsAQuoteAndAWorkingSource(t *testing.T) {
	ctx := turnContext(optIn)
	args := map[string]any{"action": "update", "revision": 0, "enabled": true, "evidence": optIn}

	offline := &fakeStore{settings: topics.Settings{Revision: 0}}
	gate := &fakeGate{}
	tool := newTool(t, offline, fakeSource{enabled: false}, gate)
	raw, _ := json.Marshal(map[string]any{"action": "update", "revision": 0, "enabled": true, "evidence": "你应该自己去找"})
	if _, err := tool.Handler(ctx, raw); err == nil {
		t.Fatal("a paraphrase enabled background search")
	}
	if _, err := tool.Handler(ctx, mustJSON(t, args)); err == nil || !strings.Contains(err.Error(), "public search") {
		t.Fatalf("enabling without a working source: %v", err)
	}
	if offline.writes != 0 {
		t.Fatal("a refused enable still reached the store")
	}

	online := &fakeStore{settings: topics.Settings{Revision: 0}}
	tool = newTool(t, online, fakeSource{enabled: true}, gate)
	out, err := tool.Handler(ctx, mustJSON(t, args))
	if err != nil || !strings.Contains(out.Output, `"enabled":true`) {
		t.Fatalf("enable = %q, %v", out.Output, err)
	}
	if online.writes != 1 {
		t.Fatal("the enable did not land")
	}

	busy := &fakeStore{settings: topics.Settings{Revision: 0}}
	tool = newTool(t, busy, fakeSource{enabled: true}, &fakeGate{err: pluginapi.ErrWriteRoleBusy})
	if _, err = tool.Handler(ctx, mustJSON(t, args)); err == nil || !strings.Contains(err.Error(), "同角色其他对话正在运行") {
		t.Fatalf("busy gate: %v", err)
	}
	if busy.writes != 0 {
		t.Fatal("a busy role still wrote")
	}
}

func TestReadNeverWrites(t *testing.T) {
	store := &fakeStore{settings: topics.Settings{Revision: 3}}
	gate := &fakeGate{}
	tool := newTool(t, store, fakeSource{}, gate)
	out, err := tool.Handler(turnContext(""), mustJSON(t, map[string]any{"action": "read"}))
	if err != nil || !strings.Contains(out.Output, `"revision":3`) {
		t.Fatalf("read = %q, %v", out.Output, err)
	}
	if store.writes != 0 || gate.calls != 0 {
		t.Fatal("a read reached the write path")
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
