package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"chat-agent/internal/pluginapi"
	"chat-agent/internal/probe"
)

func TestApprovalResumesOriginalWorkspaceCall(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Messages []probe.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) == 0 {
			t.Error("model request was invalid")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if input.Messages[len(input.Messages)-1].Role == "tool" {
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Finished\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		call := `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"write-fixture","type":"function","function":{"name":"workspace_write","arguments":"{\"workspace\":\"server\",\"path\":\"fixture.txt\",\"content\":\"confirmed\",\"expected_sha256\":\"missing\"}"}}]},"finish_reason":"tool_calls"}]}`
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", call)
	}))
	defer upstream.Close()
	root := t.TempDir()
	policy, err := pluginapi.NewPolicy(nil, []string{"builtin.workspace:filesystem:workspace://server"})
	if err != nil {
		t.Fatal(err)
	}
	config := probe.DefaultConfig()
	config.Model, config.BaseURL, config.APIKey = "fixture", upstream.URL+"/v1", "fixture-key"
	const token = "fixture-approval-token-for-the-live-run"
	app, err := New(Config{Agent: config, DataDir: t.TempDir(), AccessToken: token, Plugins: policy,
		HostTools: HostToolsConfig{WorkspaceRoot: root, AutoGrant: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close(context.Background()) })
	handler := app.Handler()
	request := func(method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: status %d, want %d: %s", method, path, w.Code, want, w.Body)
		}
		return w
	}
	var session sessionInfo
	if err := json.Unmarshal(request("POST", "/api/sessions", `{}`, 201).Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	request("POST", "/api/sessions/"+session.ID+"/turns", `{"request_id":"confirm-turn","content":"写入样例"}`, 202)
	await := func(check func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if check() {
				return
			}
			time.Sleep(15 * time.Millisecond)
		}
		items, _, _ := app.turns.List(session.ID)
		t.Fatalf("turn did not reach the expected state: %+v", items)
	}
	var pending *probe.ApprovalRequest
	await(func() bool {
		items, _, _ := app.turns.List(session.ID)
		if len(items) != 1 || len(items[0].Tools) == 0 {
			return false
		}
		pending = items[0].Tools[0].Approval
		return pending != nil && pending.ID != ""
	})
	path := filepath.Join(root, "fixture.txt")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("tool ran before approval: %v", err)
	}
	approve := func(digest string, want int) {
		body, _ := json.Marshal(map[string]string{"session": session.ID, "tool": pending.Tool, "digest": digest, "id": pending.ID})
		request("POST", "/api/approvals", string(body), want)
	}
	approve(strings.Repeat("0", 64), 409)
	approve(pending.Digest, 200)
	await(func() bool {
		items, _, _ := app.turns.List(session.ID)
		return len(items) == 1 && items[0].Status == "completed" && len(items[0].Tools) == 1 && items[0].Tools[0].Status == "completed"
	})
	approve(pending.Digest, 409)
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "confirmed" {
		t.Fatalf("original operation did not commit: %q, %v", data, err)
	}
}

// The confirmation endpoint is the one place consent enters the runtime, so its
// input is checked before anything is looked up or issued.
func TestApproveCallRejectsMalformedPayload(t *testing.T) {
	server := &Server{approvals: probe.NewApprovalStore()}
	cases := []string{
		`{}`,
		`{"session":"s1","tool":"workspace_delete","digest":"short"}`,
		`{"session":"s1","tool":"Workspace Delete","digest":"` + strings.Repeat("a", 64) + `"}`,
		`{"session":"s1","tool":"workspace_delete","digest":"` + strings.Repeat("A", 64) + `"}`,
		`{"session":"","tool":"t","digest":"` + strings.Repeat("a", 64) + `"}`,
		`{"session":"s1","tool":"t","digest":"` + strings.Repeat("a", 64) + `","extra":1}`,
	}
	for _, body := range cases {
		rec := httptest.NewRecorder()
		server.approveCall(rec, httptest.NewRequest("POST", "/api/approvals", strings.NewReader(body)))
		if rec.Code != 400 {
			t.Fatalf("%s: got %d, want 400", body, rec.Code)
		}
	}
}
