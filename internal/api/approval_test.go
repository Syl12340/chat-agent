package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"pocket-agent/internal/probe"
)

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
