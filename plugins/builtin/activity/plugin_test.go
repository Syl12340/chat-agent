package activity

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

// fakeStore keeps the two contracts the real store enforces: an idempotency key
// never creates twice, and a write needs the current revision plus the barrier.
type fakeStore struct {
	mu        sync.Mutex
	items     []companion.Activity
	bindings  map[string]string
	keys      map[string]string
	lastCuts  map[string]int
	openCalls int
	patches   int
}

func newStore() *fakeStore {
	return &fakeStore{bindings: map[string]string{}, keys: map[string]string{}}
}

func (f *fakeStore) List(string) ([]companion.Activity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]companion.Activity{}, f.items...), nil
}

func (f *fakeStore) Session(_, sessionID string) (*companion.Activity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.bindings[sessionID]
	if !ok {
		return nil, nil
	}
	for _, item := range f.items {
		if item.ID == id {
			copy := item
			return &copy, nil
		}
	}
	return nil, nil
}

func (f *fakeStore) OpenForSession(_, sessionID, id string, revision int, draft companion.ActivityDraft, cuts map[string]int) (companion.Activity, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.openCalls++
	f.lastCuts = cuts
	if id == "" && draft.IdempotencyKey != "" {
		if existing, ok := f.keys[draft.IdempotencyKey]; ok {
			for _, item := range f.items {
				if item.ID == existing {
					return item, false, nil
				}
			}
		}
	}
	if id != "" {
		for i := range f.items {
			if f.items[i].ID == id {
				if f.items[i].Revision != revision {
					return companion.Activity{}, false, companion.ErrConflict
				}
				f.items[i].Status = "active"
				f.items[i].Revision++
				f.bindings[sessionID] = id
				return f.items[i], false, nil
			}
		}
		return companion.Activity{}, false, companion.ErrActivityNotFound
	}
	item := companion.Activity{ID: "activity-one", Kind: draft.Kind, Title: draft.Title, Goal: draft.Goal, Status: "active", Revision: 1}
	f.items = append(f.items, item)
	f.keys[draft.IdempotencyKey] = item.ID
	f.bindings[sessionID] = item.ID
	return item, true, nil
}

func (f *fakeStore) Patch(_, id string, revision int, p companion.ActivityPatch, cuts map[string]int) (companion.Activity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.patches++
	f.lastCuts = cuts
	for i := range f.items {
		if f.items[i].ID != id {
			continue
		}
		if f.items[i].Revision != revision {
			return companion.Activity{}, companion.ErrConflict
		}
		if p.Progress != nil {
			f.items[i].Progress = *p.Progress
		}
		if p.Status != nil {
			f.items[i].Status = *p.Status
		}
		f.items[i].Revision++
		return f.items[i], nil
	}
	return companion.Activity{}, companion.ErrActivityNotFound
}

type fakeGate struct {
	cuts  map[string]int
	err   error
	calls int
}

func (g *fakeGate) WithRoleWrite(_, _ string, write func(pluginapi.WriteScope) error) error {
	g.calls++
	if g.err != nil {
		return g.err
	}
	return write(pluginapi.WriteScope{Cuts: g.cuts})
}

func newTools(t *testing.T, store Store, gate pluginapi.WriteGate) map[string]pluginapi.Tool {
	t.Helper()
	loader := pluginapi.NewLoader()
	loader.Add(New(store, gate))
	catalog, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]pluginapi.Tool{}
	for _, name := range []string{"shared_activity_open", "shared_activity_update"} {
		loaded, ok := catalog.Tool(name)
		if !ok {
			t.Fatalf("%s did not load", name)
		}
		out[name] = loaded.Tool
	}
	return out
}

func turnContext(session, text string) context.Context {
	ctx := probe.ToolContext(context.Background(), session, "companion", "call-1", "")
	return probe.WithToolInputs(ctx, []probe.Message{{Role: "user", Content: text}})
}

const start = "咱们一起读那本书吧，从序章开始。"

func call(t *testing.T, tool pluginapi.Tool, ctx context.Context, args map[string]any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Handler(ctx, raw)
	return result.Output, err
}

func TestCreateNeedsEvidenceAndCarriesTheBarrier(t *testing.T) {
	store, gate := newStore(), &fakeGate{cuts: map[string]int{"session-1": 7, "session-2": 3}}
	tools := newTools(t, store, gate)
	ctx := turnContext("session-1", start)
	create := map[string]any{"action": "create", "title": "一起读书", "kind": "interest", "goal": "按自己的节奏读"}

	if _, err := call(t, tools["shared_activity_open"], ctx, withEvidence(create, "")); err == nil {
		t.Fatal("a create without a quote was accepted")
	}
	if _, err := call(t, tools["shared_activity_open"], ctx, withEvidence(create, "一起读书吧")); err == nil {
		t.Fatal("a paraphrase was accepted as evidence")
	}
	if store.openCalls != 0 || gate.calls != 0 {
		t.Fatal("a refused create reached the store or the gate")
	}
	out, err := call(t, tools["shared_activity_open"], ctx, withEvidence(create, start))
	if err != nil || !strings.Contains(out, `"created":true`) {
		t.Fatalf("create = %q, %v", out, err)
	}
	// The barrier the host measured under the lock is the one that lands.
	if store.lastCuts["session-1"] != 7 || store.lastCuts["session-2"] != 3 {
		t.Fatalf("cuts = %+v", store.lastCuts)
	}
	// The same request repeated in the same turn is the same intent.
	if out, err = call(t, tools["shared_activity_open"], ctx, withEvidence(create, start)); err != nil || !strings.Contains(out, `"created":false`) {
		t.Fatalf("repeat = %q, %v", out, err)
	}
	if len(store.items) != 1 {
		t.Fatalf("activities = %d, a repeated open duplicated one", len(store.items))
	}
}

func TestUpdateRequiresABindingAndTheCurrentRevision(t *testing.T) {
	store, gate := newStore(), &fakeGate{cuts: map[string]int{"session-1": 4}}
	tools := newTools(t, store, gate)
	ctx := turnContext("session-1", "读完序章了，下回从第一章继续。")
	save := map[string]any{"revision": 1, "progress": "读完序章"}

	// Nothing is bound to this conversation yet.
	if _, err := call(t, tools["shared_activity_update"], ctx, withEvidence(save, "读完序章了")); err == nil {
		t.Fatal("update without a bound activity was accepted")
	}
	if store.patches != 0 {
		t.Fatal("an unbound update reached the store")
	}
	if _, err := call(t, tools["shared_activity_open"], ctx, withEvidence(map[string]any{"action": "create", "title": "一起读书", "kind": "interest", "goal": "慢慢读"}, "读完序章了")); err != nil {
		t.Fatal(err)
	}
	out, err := call(t, tools["shared_activity_update"], ctx, withEvidence(save, "读完序章了"))
	if err != nil || !strings.Contains(out, `"progress_saved":true`) || !strings.Contains(out, "读完序章") {
		t.Fatalf("update = %q, %v", out, err)
	}
	if store.lastCuts["session-1"] != 4 {
		t.Fatalf("update lost the barrier: %+v", store.lastCuts)
	}
	// The stored revision moved on; replaying the same call is now stale.
	if _, err := call(t, tools["shared_activity_update"], ctx, withEvidence(save, "读完序章了")); err == nil {
		t.Fatal("a stale revision was accepted")
	}
}

func TestListIsReadOnlyAndNeverTouchesTheGate(t *testing.T) {
	store, gate := newStore(), &fakeGate{}
	store.items = []companion.Activity{{ID: "activity-one", Title: "一起读书", Goal: "慢慢读", Progress: "读完序章", Status: "paused", Revision: 2}}
	tools := newTools(t, store, gate)
	out, err := call(t, tools["shared_activity_open"], turnContext("session-9", ""), map[string]any{"action": "list", "query": "读书"})
	if err != nil || !strings.Contains(out, "读完序章") || !strings.Contains(out, `"status":"paused"`) {
		t.Fatalf("list = %q, %v", out, err)
	}
	if gate.calls != 0 || len(store.bindings) != 0 {
		t.Fatal("a read resumed or bound an activity")
	}
}

func TestRefusedGateNeverWrites(t *testing.T) {
	for _, row := range []struct {
		name string
		err  error
		want string
	}{
		{"ended", pluginapi.ErrWriteSessionEnded, "对话已停止"},
		{"busy", pluginapi.ErrWriteRoleBusy, "同角色其他对话正在运行"},
	} {
		store := newStore()
		tools := newTools(t, store, &fakeGate{err: row.err})
		_, err := call(t, tools["shared_activity_open"], turnContext("session-1", start), withEvidence(map[string]any{"action": "create", "title": "一起读书", "kind": "interest", "goal": "慢慢读"}, start))
		if err == nil || !strings.Contains(err.Error(), row.want) {
			t.Fatalf("%s: err = %v", row.name, err)
		}
		if len(store.items) != 0 {
			t.Fatalf("%s: a refused create landed", row.name)
		}
	}
}

func withEvidence(args map[string]any, evidence string) map[string]any {
	out := map[string]any{"evidence": evidence}
	for key, value := range args {
		out[key] = value
	}
	return out
}
