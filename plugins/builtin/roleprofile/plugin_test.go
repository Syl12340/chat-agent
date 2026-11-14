package roleprofile

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"chat-agent/internal/pluginapi"
	"chat-agent/internal/probe"
	"chat-agent/internal/roles"
)

type fakeProfiles struct {
	mu       sync.Mutex
	profile  string
	proposed int
	conflict bool
	lastBase string
}

func (f *fakeProfiles) Load(string) (roles.Role, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return roles.Role{ID: "companion", Profile: f.profile}, nil
}

func (f *fakeProfiles) ProposeProfile(role, baseHash, content, reason, sessionID string) (roles.Proposal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastBase = baseHash
	if f.conflict {
		return roles.Proposal{}, roles.ErrProposalConflict
	}
	f.proposed++
	return roles.Proposal{ID: "proposal-one", RoleID: role, File: "PROFILE.md", Content: content, Reason: reason, Status: "pending", Revision: 1, SourceSessionID: sessionID}, nil
}

func newTool(t *testing.T, profiles Profiles) pluginapi.Tool {
	t.Helper()
	loader := pluginapi.NewLoader()
	loader.Add(New(profiles))
	catalog, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	loaded, ok := catalog.Tool("role_change_propose")
	if !ok {
		t.Fatal("role_change_propose did not load")
	}
	return loaded.Tool
}

func turnContext(text string) context.Context {
	ctx := probe.ToolContext(context.Background(), "session-1", "companion", "call-1", "")
	return probe.WithToolInputs(ctx, []probe.Message{{Role: "user", Content: text}})
}

const request = "以后你的档案里就写你叫大肥鱼吧，别再叫别的名字。"

func call(t *testing.T, tool pluginapi.Tool, args map[string]any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Handler(turnContext(request), raw)
	return result.Output, err
}

func TestProposalNeedsAQuoteAndStaysPending(t *testing.T) {
	profiles := &fakeProfiles{profile: "你是 chat-agent。"}
	tool := newTool(t, profiles)
	body := map[string]any{"content": "你叫大肥鱼。", "reason": "用户要求改名"}

	for _, evidence := range []string{"", "档案里写你叫大肥鱼"} {
		args := map[string]any{"evidence": evidence}
		for key, value := range body {
			args[key] = value
		}
		if _, err := call(t, tool, args); err == nil {
			t.Fatalf("proposal accepted evidence %q", evidence)
		}
	}
	if profiles.proposed != 0 {
		t.Fatal("an unquoted proposal reached the store")
	}
	args := map[string]any{"evidence": request}
	for key, value := range body {
		args[key] = value
	}
	out, err := call(t, tool, args)
	if err != nil {
		t.Fatal(err)
	}
	// Pending review is the whole point: the tool must never report a change.
	if !strings.Contains(out, `"status":"pending"`) || !strings.Contains(out, `"requires_user_review":true`) {
		t.Fatalf("proposal = %q", out)
	}
	if profiles.proposed != 1 {
		t.Fatalf("proposed = %d", profiles.proposed)
	}
	// The proposal is bound to the profile version it was written against.
	if profiles.lastBase != roles.ProfileHash("你是 chat-agent。") {
		t.Fatalf("base hash = %q", profiles.lastBase)
	}
}

func TestProfileChangedElsewhereIsReported(t *testing.T) {
	profiles := &fakeProfiles{profile: "你是 chat-agent。", conflict: true}
	tool := newTool(t, profiles)
	_, err := call(t, tool, map[string]any{"content": "你叫大肥鱼。", "reason": "改名", "evidence": request})
	if err == nil || !strings.Contains(err.Error(), "角色档案已更新") {
		t.Fatalf("err = %v", err)
	}
}

func TestMissingProfilesDropsTheTool(t *testing.T) {
	if New(nil) != nil {
		t.Fatal("roleprofile loaded without a role manager")
	}
}
