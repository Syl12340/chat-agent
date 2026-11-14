package episode

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"chat-agent/internal/companion"
	"chat-agent/internal/pluginapi"
	"chat-agent/internal/probe"
)

type fakeStore struct {
	drafts []companion.EpisodeDraft
}

func (f *fakeStore) AddEpisode(_ string, draft companion.EpisodeDraft) (companion.Episode, error) {
	f.drafts = append(f.drafts, draft)
	return companion.Episode{ID: "episode-one", Title: draft.Title, Content: draft.Content, Status: draft.Status, Revision: 1}, nil
}

func newTool(t *testing.T, store Store) pluginapi.Tool {
	t.Helper()
	loader := pluginapi.NewLoader()
	loader.Add(New(store))
	catalog, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	loaded, ok := catalog.Tool("episode_propose")
	if !ok {
		t.Fatal("episode_propose did not load")
	}
	return loaded.Tool
}

func turnContext(text string) context.Context {
	ctx := probe.ToolContext(context.Background(), "session-1", "companion", "call-1", "")
	return probe.WithToolInputs(ctx, []probe.Message{{Role: "user", Content: text}})
}

const ask = "下周三我要去面试，你到时候提醒我一下。"

func TestProposeNeedsTheUsersOwnWords(t *testing.T) {
	store := &fakeStore{}
	tool := newTool(t, store)
	ctx := turnContext(ask)
	for _, evidence := range []string{"", "面试的事", "好的，我记下了"} {
		raw, _ := json.Marshal(map[string]any{"title": "面试", "content": "下周三面试", "evidence": evidence})
		if _, err := tool.Handler(ctx, raw); err == nil {
			t.Fatalf("proposal accepted evidence %q", evidence)
		}
	}
	if len(store.drafts) != 0 {
		t.Fatal("an unquoted proposal reached the store")
	}
	when := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	raw, _ := json.Marshal(map[string]any{"title": "面试", "content": "下周三去面试", "evidence": ask, "event_at": when, "allow_followup": true, "followup_at": when.Add(-24 * time.Hour)})
	out, err := tool.Handler(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.drafts) != 1 {
		t.Fatalf("drafts = %d", len(store.drafts))
	}
	draft := store.drafts[0]
	// A proposal is always pending: the panel decides, never the model.
	if draft.Status != "pending" || draft.SourceSessionID != "session-1" || draft.SourceText != ask {
		t.Fatalf("draft = %+v", draft)
	}
	if draft.EventAt == nil || draft.FollowupAt == nil || !draft.AllowFollowup {
		t.Fatalf("followup fields lost: %+v", draft)
	}
	if out.Output == "" {
		t.Fatal("no record returned")
	}
}

func TestMissingStoreDropsTheTool(t *testing.T) {
	if New(nil) != nil {
		t.Fatal("episode loaded without a life store")
	}
}
