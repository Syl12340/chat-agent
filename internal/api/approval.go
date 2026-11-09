package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"chat-agent/internal/probe"
)

// approveCall issues the only consent this service accepts for a call that could
// not be assessed: a host-recorded, single-use ticket bound to the session's
// role, the tool and the exact argument digest.
//
// It is deliberately not a model tool. The model can neither call it nor write
// its payload: the digest must match a refusal the executor itself recorded, so
// an invented "yes" has nothing to attach to.
//
// The design follows DeepSeek Harness's user-approval seam (MIT): a one-shot
// grant, a closed outcome vocabulary, and fail-closed behaviour when nobody
// answers. The binding to arguments is this project's addition.
func (s *Server) approveCall(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Session string `json:"session"`
		Tool    string `json:"tool"`
		Digest  string `json:"digest"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil || !validID(body.Session) || !validToolName(body.Tool) || !validApprovalDigest(body.Digest) {
		fail(w, 400, "请求格式不正确")
		return
	}
	sess, err := s.session(body.Session)
	if err != nil {
		fail(w, 404, "会话不存在")
		return
	}
	ticket, err := s.approvals.Issue(body.Session, sess.RoleID(), body.Tool, body.Digest)
	if err != nil {
		switch {
		case errors.Is(err, probe.ErrApprovalUnknown):
			fail(w, 409, "这次调用没有待批准的请求，可能已经过期或已经执行过")
		case errors.Is(err, probe.ErrApprovalUnavailable):
			fail(w, 503, "授权暂时不可用")
		default:
			fail(w, 500, "无法签发授权")
		}
		return
	}
	writeJSON(w, 200, map[string]any{"approval": ticket})
}

// validToolName keeps the confirmation payload to names this build can register.
func validToolName(name string) bool {
	if len(name) == 0 || len(name) > 64 || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

// validApprovalDigest accepts exactly a SHA-256 hex string; anything else cannot
// name a call the executor recorded.
func validApprovalDigest(digest string) bool {
	if len(digest) != 64 {
		return false
	}
	for _, r := range digest {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
