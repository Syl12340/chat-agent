package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pocket-agent/internal/probe"
	"pocket-agent/internal/roles"
	"pocket-agent/internal/turns"
)

type proposalFixture struct {
	t       *testing.T
	content string
}

func (p proposalFixture) Stream(_ context.Context, r probe.ModelRequest, emit func(probe.Event)) (probe.Message, error) {
	last := r.Messages[len(r.Messages)-1]
	if last.Role == "user" && last.Content == "请把交流风格改为更简短，提交审核。" {
		found := false
		for _, tool := range r.Tools {
			found = found || tool.Name == "role_change_propose"
		}
		if !found {
			p.t.Error("proposal tool absent from actual request")
		}
		args, _ := json.Marshal(map[string]string{"content": p.content, "reason": "用户要求长期简短风格", "evidence": last.Content})
		return probe.Message{Role: "assistant", ToolCalls: []probe.ToolCall{{ID: "profile-proposal", Type: "function", Function: probe.FunctionCall{Name: "role_change_propose", Arguments: string(args)}}}}, nil
	}
	if last.Role == "tool" && !strings.Contains(last.Content, `"status":"pending"`) {
		p.t.Errorf("proposal did not stay pending: %s", last.Content)
	}
	emit(probe.Event{Type: probe.EventTextDelta, Text: "已提交建议，等待审核。"})
	return probe.Message{Role: "assistant", Content: "已提交建议，等待审核。"}, nil
}

func TestProposalReviewAndConversationDeletion(t *testing.T) {
	const token = "proposal-delete-local-fixture-token-over-32"
	c := probe.DefaultConfig()
	c.Model = "fixture"
	c.Provider = proposalFixture{t, "# 风格\n保持简短。"}
	s, err := New(Config{Agent: c, DataDir: t.TempDir(), AccessToken: token})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	request := func(method, path, body string, want int) []byte {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		out := httptest.NewRecorder()
		s.Handler().ServeHTTP(out, req)
		if out.Code != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, out.Code, want, out.Body.String())
		}
		return out.Body.Bytes()
	}
	var session struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal(request("POST", "/api/sessions", `{"role_id":"companion"}`, 201), &session); err != nil {
		t.Fatal(err)
	}
	base := "/api/sessions/" + session.ID
	before, _ := s.roles.Load("companion")
	request("POST", base+"/turns", `{"request_id":"proposal-turn","content":"请把交流风格改为更简短，提交审核。"}`, 202)
	wait := func(count int) {
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			var history struct {
				Active bool         `json:"active"`
				Turns  []turns.Turn `json:"turns"`
			}
			if err = json.Unmarshal(request("GET", base+"/messages", "", 200), &history); err != nil {
				t.Fatal(err)
			}
			if !history.Active && len(history.Turns) == count {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("fixture turn did not settle")
	}
	wait(1)
	unchanged, _ := s.roles.Load("companion")
	if unchanged.Profile != before.Profile {
		t.Fatal("proposal changed unapproved profile")
	}
	var proposals []roles.Proposal
	if err = json.Unmarshal(request("GET", "/api/roles/companion/proposals", "", 200), &proposals); err != nil || len(proposals) != 1 {
		t.Fatalf("missing candidate: %v", err)
	}
	p := proposals[0]
	endpoint := "/api/roles/companion/proposals/" + p.ID
	request("PATCH", "/api/roles/planner/proposals/"+p.ID, `{"revision":1,"action":"approve"}`, 404)
	request("PATCH", endpoint, `{"revision":1,"action":"approve"}`, 200)
	after, _ := s.roles.Load("companion")
	if after.Profile != p.Content || after.Rules != before.Rules {
		t.Fatal("review did not apply only PROFILE")
	}
	request("POST", base+"/turns", `{"request_id":"second-turn","content":"保留这条"}`, 202)
	wait(2)
	request("DELETE", base+"/turns/proposal-turn?purge=1", "", 409)
	request("DELETE", base+"/turns/second-turn?purge=1", "", 200)
	s.mu.Lock()
	sess := s.sessions[session.ID]
	delete(s.sessions, session.ID)
	s.mu.Unlock()
	if err = sess.Close(); err != nil {
		t.Fatal(err)
	}
	// A fresh read must use the rewritten journal and surviving turn indexes.
	request("GET", base+"/messages", "", 200)
	s.mu.Lock()
	s.runs[session.ID] = &runState{}
	s.mu.Unlock()
	request("DELETE", base, "", 409)
	s.mu.Lock()
	delete(s.runs, session.ID)
	s.mu.Unlock()
	request("DELETE", base, "", 200)
	if _, err = os.Stat(filepath.Join(s.config.DataDir, probe.AgentID, session.ID+".jsonl")); !os.IsNotExist(err) {
		t.Fatalf("journal remains after delete: %v", err)
	}
	kept, _ := s.roles.Load("companion")
	if kept.Profile != p.Content {
		t.Fatal("deleting chat removed role profile")
	}
	items, err := s.roles.Proposals("companion")
	if err != nil || len(items) != 1 || items[0].Status != "applied" {
		t.Fatal("deleting chat removed independent proposal history")
	}
}
