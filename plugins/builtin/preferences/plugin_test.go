package preferences

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"chat-agent/internal/companion"
	"chat-agent/internal/pluginapi"
	"chat-agent/internal/probe"
)

// fakeStore keeps the revision contract the companion store enforces: a patch
// built on an old revision is refused rather than silently applied.
type fakeStore struct {
	mu       sync.Mutex
	snapshot companion.Snapshot
	writes   int
	conflict bool
}

func (f *fakeStore) Get(string) (companion.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshot, nil
}

func (f *fakeStore) Update(_ string, patch companion.Patch) (companion.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.conflict {
		return companion.Snapshot{}, companion.ErrConflict
	}
	if patch.Revision != f.snapshot.Revision {
		return companion.Snapshot{}, companion.ErrConflict
	}
	f.writes++
	if patch.ReplyLength != nil {
		f.snapshot.Preferences.ReplyLength = *patch.ReplyLength
	}
	if patch.Tone != nil {
		f.snapshot.Preferences.Tone = *patch.Tone
	}
	f.snapshot.Revision++
	return f.snapshot, nil
}

func newTool(t *testing.T, store Store) pluginapi.Tool {
	t.Helper()
	loader := pluginapi.NewLoader()
	loader.Add(New(store))
	catalog, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	loaded, ok := catalog.Tool("companion_preferences_update")
	if !ok {
		t.Fatal("companion_preferences_update did not load")
	}
	return loaded.Tool
}

func turnContext(text string) context.Context {
	ctx := probe.ToolContext(context.Background(), "session-1", "companion", "call-1", "")
	return probe.WithToolInputs(ctx, []probe.Message{{Role: "user", Content: text}})
}

const request = "以后回答我短一点，别写那么长。"

func call(t *testing.T, tool pluginapi.Tool, ctx context.Context, args map[string]any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Handler(ctx, raw)
	return result.Output, err
}

func TestSavingNeedsTheUsersOwnWords(t *testing.T) {
	store := &fakeStore{snapshot: companion.Snapshot{Revision: 4}}
	tool := newTool(t, store)
	ctx := turnContext(request)
	for _, row := range []struct {
		name string
		args map[string]any
	}{
		{"unquoted", map[string]any{"revision": 4, "reply_length": "short", "evidence": ""}},
		{"paraphrase", map[string]any{"revision": 4, "reply_length": "short", "evidence": "回答短一点"}},
		{"assistant words", map[string]any{"revision": 4, "reply_length": "short", "evidence": "好的，我以后简短一些"}},
		{"stale", map[string]any{"revision": 3, "reply_length": "short", "evidence": request}},
	} {
		if _, err := call(t, tool, ctx, row.args); err == nil {
			t.Fatalf("%s: a preference write was accepted", row.name)
		}
	}
	if store.writes != 0 {
		t.Fatal("a refused write reached the store")
	}
	out, err := call(t, tool, ctx, map[string]any{"revision": 4, "reply_length": "short", "evidence": request})
	if err != nil || !strings.Contains(out, `"reply_length":"short"`) {
		t.Fatalf("save = %q, %v", out, err)
	}
	if store.writes != 1 {
		t.Fatalf("writes = %d", store.writes)
	}
}

func TestStoreConflictIsReportedAsSuch(t *testing.T) {
	store := &fakeStore{snapshot: companion.Snapshot{Revision: 2}, conflict: true}
	tool := newTool(t, store)
	_, err := call(t, tool, turnContext(request), map[string]any{"revision": 2, "tone": "warm", "evidence": request})
	if err == nil || !strings.Contains(err.Error(), "偏好已在别处修改") {
		t.Fatalf("err = %v", err)
	}
}

func TestMissingStoreDropsTheTool(t *testing.T) {
	if New(nil) != nil {
		t.Fatal("preferences loaded without a companion store")
	}
}
