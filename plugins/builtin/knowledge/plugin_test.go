package knowledge

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"chat-agent/internal/companion"
	"chat-agent/internal/memory"
	"chat-agent/internal/pluginapi"
	"chat-agent/internal/probe"
)

// fakeFacts keeps the revision contract the memory store enforces.
type fakeFacts struct {
	mu      sync.Mutex
	entries map[string]memory.Entry
	updates int
	deleted int
	merged  int
}

func newFacts(items ...memory.Entry) *fakeFacts {
	store := &fakeFacts{entries: map[string]memory.Entry{}}
	for _, item := range items {
		store.entries[item.ID] = item
	}
	return store
}

func (f *fakeFacts) FindContext(_ context.Context, _, _, status string, _ int) ([]memory.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []memory.Entry{}
	for _, item := range f.entries {
		if status == "" || status == "approved" {
			if item.Status == "approved" {
				out = append(out, item)
			}
			continue
		}
		if status == "all" || item.Status == status {
			out = append(out, item)
		}
	}
	return out, nil
}

func (f *fakeFacts) Get(_, id string) (memory.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	item, ok := f.entries[id]
	if !ok {
		return memory.Entry{}, memory.ErrNotFound
	}
	return item, nil
}

func (f *fakeFacts) Update(_, id, content, status string, revision int) (memory.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	item, ok := f.entries[id]
	if !ok || item.Revision != revision {
		return memory.Entry{}, memory.ErrConflict
	}
	f.updates++
	item.Content, item.Status, item.Revision = content, status, item.Revision+1
	f.entries[id] = item
	return item, nil
}

func (f *fakeFacts) Delete(_, id string, revision int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	item, ok := f.entries[id]
	if !ok || item.Revision != revision {
		return memory.ErrConflict
	}
	f.deleted++
	delete(f.entries, id)
	return nil
}

func (f *fakeFacts) Merge(_ string, refs []memory.Reference, content string) (memory.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ref := range refs {
		item, ok := f.entries[ref.ID]
		if !ok || item.Revision != ref.Revision {
			return memory.Entry{}, memory.ErrConflict
		}
	}
	f.merged++
	first := f.entries[refs[0].ID]
	first.Content, first.Revision = content, first.Revision+1
	return first, nil
}

type fakeLife struct {
	mu       sync.Mutex
	episodes map[string]companion.Episode
	cuts     map[string]int
	advanced int
	replaced int
	deleted  int
}

func newLife(items ...companion.Episode) *fakeLife {
	store := &fakeLife{episodes: map[string]companion.Episode{}, cuts: map[string]int{}}
	for _, item := range items {
		store.episodes[item.ID] = item
	}
	return store
}

func (f *fakeLife) Episodes(string) ([]companion.Episode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []companion.Episode{}
	for _, item := range f.episodes {
		out = append(out, item)
	}
	return out, nil
}

func (f *fakeLife) Episode(_, id string) (companion.Episode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	item, ok := f.episodes[id]
	if !ok {
		return companion.Episode{}, companion.ErrLifeNotFound
	}
	return item, nil
}

func (f *fakeLife) ReplaceEpisode(_, id string, revision int, draft companion.EpisodeDraft, cuts map[string]int) (companion.Episode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	item, ok := f.episodes[id]
	if !ok || item.Revision != revision {
		return companion.Episode{}, companion.ErrConflict
	}
	f.replaced++
	f.cuts = cuts
	item.Title, item.Content, item.Status, item.Revision = draft.Title, draft.Content, draft.Status, item.Revision+1
	f.episodes[id] = item
	return item, nil
}

func (f *fakeLife) DeleteEpisode(_, id string, revision int, cuts map[string]int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	item, ok := f.episodes[id]
	if !ok || item.Revision != revision {
		return companion.ErrConflict
	}
	f.deleted++
	f.cuts = cuts
	delete(f.episodes, id)
	return nil
}

func (f *fakeLife) AdvanceCuts(_ string, cuts map[string]int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.advanced++
	f.cuts = cuts
	return nil
}

// fakeGate hands the tool a fixed barrier and can refuse like the host does.
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

func newTool(t *testing.T, facts Memory, events Life, gate pluginapi.WriteGate) pluginapi.Tool {
	t.Helper()
	loader := pluginapi.NewLoader()
	loader.Add(New(facts, events, gate))
	catalog, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	loaded, ok := catalog.Tool("knowledge_manage")
	if !ok {
		t.Fatal("knowledge_manage did not load")
	}
	return loaded.Tool
}

func turnContext(text string) context.Context {
	ctx := probe.ToolContext(context.Background(), "session-1", "companion", "call-1", "")
	return probe.WithToolInputs(ctx, []probe.Message{{Role: "user", Content: text}})
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

const correction = "那个地址记错了，是文史楼三楼，不是二楼。"

func TestFactWritePublishesTheBarrierBeforeTouchingMemory(t *testing.T) {
	facts := newFacts(memory.Entry{ID: "fact-one", Content: "面试在文史楼二楼", Status: "approved", Revision: 3})
	events := newLife()
	gate := &fakeGate{cuts: map[string]int{"session-1": 12, "session-2": 4}}
	tool := newTool(t, facts, events, gate)
	ctx := turnContext(correction)

	// No quote, a paraphrase, and a stale revision are all refusals, and none of
	// them may publish a barrier or touch the store.
	for _, row := range []struct {
		name string
		args map[string]any
	}{
		{"unquoted", map[string]any{"operation": "update", "kind": "fact", "id": "fact-one", "revision": 3, "content": "文史楼三楼", "evidence": ""}},
		{"paraphrase", map[string]any{"operation": "update", "kind": "fact", "id": "fact-one", "revision": 3, "content": "文史楼三楼", "evidence": "把地址改一下"}},
		{"stale", map[string]any{"operation": "update", "kind": "fact", "id": "fact-one", "revision": 2, "content": "文史楼三楼", "evidence": correction}},
		{"status", map[string]any{"operation": "update", "kind": "fact", "id": "fact-one", "revision": 3, "content": "文史楼三楼", "evidence": correction, "status": "approved"}},
	} {
		if _, err := call(t, tool, ctx, row.args); err == nil {
			t.Fatalf("%s: write was accepted", row.name)
		}
	}
	if facts.updates != 0 || events.advanced != 0 {
		t.Fatalf("a refused write touched the store: updates=%d barriers=%d", facts.updates, events.advanced)
	}
	out, err := call(t, tool, ctx, map[string]any{"operation": "update", "kind": "fact", "id": "fact-one", "revision": 3, "content": "面试在文史楼三楼", "evidence": correction})
	if err != nil || !strings.Contains(out, `"operation":"updated"`) {
		t.Fatalf("update = %q, %v", out, err)
	}
	if facts.updates != 1 {
		t.Fatalf("updates = %d", facts.updates)
	}
	// The counts the host measured under the lock are the ones published, so the
	// corrected sentence cannot come back through either conversation.
	if events.advanced != 1 || events.cuts["session-1"] != 12 || events.cuts["session-2"] != 4 {
		t.Fatalf("barrier = %+v (advanced %d)", events.cuts, events.advanced)
	}
}

func TestForgetAndMergeKeepTheirRefusals(t *testing.T) {
	facts := newFacts(
		memory.Entry{ID: "fact-one", Content: "喜欢喝咖啡", Status: "approved", Revision: 1},
		memory.Entry{ID: "fact-two", Content: "喜欢喝茶", Status: "approved", Revision: 1},
		memory.Entry{ID: "fact-three", Content: "待审核的猜测", Status: "pending", Revision: 1},
	)
	events := newLife()
	tool := newTool(t, facts, events, &fakeGate{cuts: map[string]int{"session-1": 2}})
	ctx := turnContext(correction)

	if _, err := call(t, tool, ctx, map[string]any{"operation": "merge", "kind": "fact", "references": []any{map[string]any{"id": "fact-one", "revision": 1}, map[string]any{"id": "fact-three", "revision": 1}}, "content": "喜欢喝茶和咖啡", "evidence": correction}); err == nil {
		t.Fatal("merged records in different review states")
	}
	if facts.merged != 0 {
		t.Fatal("a refused merge landed")
	}
	if _, err := call(t, tool, ctx, map[string]any{"operation": "merge", "kind": "fact", "references": []any{map[string]any{"id": "fact-one", "revision": 1}, map[string]any{"id": "fact-two", "revision": 1}}, "content": "喜欢喝茶和咖啡", "evidence": correction}); err != nil {
		t.Fatal(err)
	}
	if facts.merged != 1 {
		t.Fatal("merge did not land")
	}
	if _, err := call(t, tool, ctx, map[string]any{"operation": "forget", "kind": "fact", "id": "fact-two", "revision": 1, "evidence": correction}); err != nil {
		t.Fatal(err)
	}
	if facts.deleted != 1 {
		t.Fatal("forget did not land")
	}
}

func TestPendingEventCannotBeApprovedFromChat(t *testing.T) {
	facts := newFacts()
	events := newLife(companion.Episode{ID: "episode-one", Title: "面试", Content: "下周三", Status: "pending", Revision: 2})
	tool := newTool(t, facts, events, &fakeGate{cuts: map[string]int{"session-1": 5}})
	ctx := turnContext(correction)
	if _, err := call(t, tool, ctx, map[string]any{"operation": "update", "kind": "event", "id": "episode-one", "revision": 2, "status": "open", "evidence": correction}); err == nil {
		t.Fatal("chat approved a pending event")
	}
	if events.replaced != 0 {
		t.Fatal("a refused event update landed")
	}
	out, err := call(t, tool, ctx, map[string]any{"operation": "update", "kind": "event", "id": "episode-one", "revision": 2, "title": "面试改到周四", "evidence": correction})
	if err != nil || !strings.Contains(out, `"operation":"updated"`) {
		t.Fatalf("event update = %q, %v", out, err)
	}
	if events.replaced != 1 || events.cuts["session-1"] != 5 {
		t.Fatalf("event write lost the barrier: replaced=%d cuts=%+v", events.replaced, events.cuts)
	}
}

func TestRefusedGateNeverWrites(t *testing.T) {
	for _, row := range []struct {
		name string
		err  error
		want string
	}{
		{"ended", pluginapi.ErrWriteSessionEnded, "对话已停止"},
		{"busy", pluginapi.ErrWriteRoleBusy, "同角色的另一段对话"},
		{"context", pluginapi.ErrWriteContextUnavailable, "上下文不可用"},
	} {
		facts := newFacts(memory.Entry{ID: "fact-one", Content: "旧内容", Status: "approved", Revision: 1})
		events := newLife()
		tool := newTool(t, facts, events, &fakeGate{err: row.err})
		_, err := call(t, tool, turnContext(correction), map[string]any{"operation": "update", "kind": "fact", "id": "fact-one", "revision": 1, "content": "新内容", "evidence": correction})
		if err == nil || !strings.Contains(err.Error(), row.want) {
			t.Fatalf("%s: err = %v", row.name, err)
		}
		if facts.updates != 0 || events.advanced != 0 {
			t.Fatalf("%s: a refused write reached the store", row.name)
		}
	}
}

func TestSearchHidesSourceText(t *testing.T) {
	facts := newFacts(memory.Entry{ID: "fact-one", Content: "面试在文史楼三楼", Status: "approved", Revision: 1})
	events := newLife(companion.Episode{ID: "episode-one", Title: "面试", Content: "下周三", Status: "open", Revision: 1, SourceText: "原始引用不该外泄"})
	tool := newTool(t, facts, events, &fakeGate{})
	ctx := turnContext("")
	out, err := call(t, tool, ctx, map[string]any{"operation": "search", "kind": "event"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "原始引用不该外泄") {
		t.Fatal("source quote leaked into a search result")
	}
	out, err = call(t, tool, ctx, map[string]any{"operation": "search", "kind": "fact"})
	if err != nil || !strings.Contains(out, "文史楼三楼") {
		t.Fatalf("fact search = %q, %v", out, err)
	}
}

func TestMissingDependenciesDropTheTool(t *testing.T) {
	facts := newFacts()
	events := newLife()
	gate := &fakeGate{}
	if New(nil, events, gate) != nil || New(facts, nil, gate) != nil || New(facts, events, nil) != nil {
		t.Fatal("knowledge loaded with a missing dependency")
	}
}
