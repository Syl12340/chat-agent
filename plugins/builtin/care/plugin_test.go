package care

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"chat-agent/internal/companion"
	"chat-agent/internal/pluginapi"
	"chat-agent/internal/probe"
)

// fakeStore keeps the revision contract the life store enforces: a write built
// on a stale revision is refused rather than silently applied.
type fakeStore struct {
	mu       sync.Mutex
	settings companion.CareSettings
	writes   int
}

func (f *fakeStore) Settings(string) (companion.CareSettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.settings, nil
}

func (f *fakeStore) SetSettings(_ string, p companion.CareSettings) (companion.CareSettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p.Revision != f.settings.Revision {
		return companion.CareSettings{}, companion.ErrConflict
	}
	p.Revision++
	f.settings = p
	f.writes++
	return p, nil
}

// fakeGate stands in for the host lock. err, when set, is what the real host
// would return after finding the turn dead or the role busy.
type fakeGate struct {
	err   error
	calls int
}

func (g *fakeGate) WithRoleWrite(_, _ string, write func() error) error {
	g.calls++
	if g.err != nil {
		return g.err
	}
	return write()
}

func newTool(t *testing.T, store Store, gate pluginapi.WriteGate) pluginapi.Tool {
	t.Helper()
	loader := pluginapi.NewLoader()
	loader.Add(New(store, gate))
	catalog, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	loaded, ok := catalog.Tool("care_preferences")
	if !ok {
		t.Fatal("care_preferences did not load")
	}
	return loaded.Tool
}

func call(t *testing.T, tool pluginapi.Tool, ctx context.Context, args map[string]any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Handler(ctx, raw)
	return result.Output, err
}

func turnContext(text string) context.Context {
	ctx := probe.ToolContext(context.Background(), "session-1", "companion", "call-1", "")
	return probe.WithToolInputs(ctx, []probe.Message{{Role: "user", Content: text}})
}

const optIn = "行，以后你每天都可以来问我一句。"

func TestReadIsFreeAndWritesNeedARealQuote(t *testing.T) {
	store := &fakeStore{settings: companion.CareSettings{Revision: 1, MinIntervalMinutes: 180, DailyLimit: 1}}
	gate := &fakeGate{}
	tool := newTool(t, store, gate)
	ctx := turnContext(optIn)

	out, err := call(t, tool, ctx, map[string]any{"action": "read"})
	if err != nil || !strings.Contains(out, `"revision":1`) {
		t.Fatalf("read = %q, %v", out, err)
	}
	if store.writes != 0 || gate.calls != 0 {
		t.Fatal("a read reached the write path")
	}
	if _, err = call(t, tool, ctx, map[string]any{"action": "read", "enabled": true}); err == nil {
		t.Fatal("a read that also changes settings was accepted")
	}
	// No quote, a paraphrase, and assistant text are all the same failure: the
	// user never said it in this turn.
	for _, evidence := range []string{"", "你以后可以每天来问我", "好的，我记住了"} {
		if _, err = call(t, tool, ctx, map[string]any{"action": "update", "revision": 1, "enabled": true, "evidence": evidence}); err == nil {
			t.Fatalf("write accepted evidence %q", evidence)
		}
	}
	if store.writes != 0 {
		t.Fatal("an unquoted write landed")
	}
	out, err = call(t, tool, ctx, map[string]any{"action": "update", "revision": 1, "enabled": true, "evidence": optIn})
	if err != nil || !strings.Contains(out, `"enabled":true`) {
		t.Fatalf("opt-in write = %q, %v", out, err)
	}
	if store.writes != 1 || gate.calls != 1 {
		t.Fatalf("writes = %d, gate calls = %d", store.writes, gate.calls)
	}
	// The revision moved, so the same arguments are now stale rather than a
	// silent second write.
	if _, err = call(t, tool, ctx, map[string]any{"action": "update", "revision": 1, "enabled": true, "evidence": optIn}); err == nil {
		t.Fatal("a stale revision was accepted")
	}
}

func TestRefusedWriteNeverReachesTheStore(t *testing.T) {
	for _, row := range []struct {
		name string
		err  error
		want string
	}{
		{"ended", pluginapi.ErrWriteSessionEnded, "对话已停止"},
		{"busy", pluginapi.ErrWriteRoleBusy, "同角色其他对话正在运行"},
	} {
		store := &fakeStore{settings: companion.CareSettings{Revision: 1, MinIntervalMinutes: 180}}
		gate := &fakeGate{err: row.err}
		tool := newTool(t, store, gate)
		_, err := call(t, tool, turnContext(optIn), map[string]any{"action": "update", "revision": 1, "enabled": true, "evidence": optIn})
		if err == nil || !strings.Contains(err.Error(), row.want) {
			t.Fatalf("%s: err = %v", row.name, err)
		}
		if store.writes != 0 {
			t.Fatalf("%s: a refused write still landed", row.name)
		}
	}
}

func TestSnoozeNeedsAFutureTimeAndKeepsThePlan(t *testing.T) {
	store := &fakeStore{settings: companion.CareSettings{Revision: 1, Enabled: true, MinIntervalMinutes: 240}}
	gate := &fakeGate{}
	tool := newTool(t, store, gate)
	ctx := turnContext(optIn)
	for _, until := range []string{"2000-01-01T09:00:00+08:00", "明天九点"} {
		args := map[string]any{"action": "snooze", "revision": 1, "evidence": optIn, "snooze_until": until}
		if _, err := call(t, tool, ctx, args); err == nil {
			t.Fatalf("snooze accepted %q", until)
		}
	}
	until := time.Now().Add(time.Hour).Format(time.RFC3339)
	if _, err := call(t, tool, ctx, map[string]any{"action": "snooze", "revision": 1, "evidence": optIn, "snooze_until": until}); err != nil {
		t.Fatal(err)
	}
	if store.settings.SnoozeUntil == nil || !store.settings.Enabled || store.settings.MinIntervalMinutes != 240 {
		t.Fatal("snooze did not preserve the plan it paused")
	}
	if _, err := call(t, tool, ctx, map[string]any{"action": "resume", "revision": store.settings.Revision, "evidence": optIn}); err != nil {
		t.Fatal(err)
	}
	if store.settings.SnoozeUntil != nil || !store.settings.Enabled {
		t.Fatal("resume did not clear snooze or changed the plan")
	}
}

func TestMissingDependenciesDropTheTool(t *testing.T) {
	if New(nil, &fakeGate{}) != nil {
		t.Fatal("care loaded without a store")
	}
	if New(&fakeStore{}, nil) != nil {
		t.Fatal("care loaded without a write gate, so it could race another conversation")
	}
}
