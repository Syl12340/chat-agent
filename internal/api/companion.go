package api

import (
	"errors"
	"net/http"

	"chat-agent/internal/companion"
)

func companionFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, companion.ErrConflict):
		fail(w, 409, "偏好已在别处修改，请重新打开后保存")
	case errors.Is(err, companion.ErrInvalid):
		fail(w, 400, "请检查称呼、偏好选项和版本；称呼最多 40 字且不能包含控制字符")
	default:
		fail(w, 503, "陪伴偏好暂时不可用，请稍后重试")
	}
}
func (s *Server) getCompanion(w http.ResponseWriter, r *http.Request) {
	if _, err := s.roles.Load(r.PathValue("id")); err != nil {
		fail(w, 404, "角色不存在")
		return
	}
	snap, err := s.companion.Get(r.PathValue("id"))
	if err != nil {
		companionFailure(w, err)
		return
	}
	writeJSON(w, 200, snap)
}
func (s *Server) updateCompanion(w http.ResponseWriter, r *http.Request) {
	if _, err := s.roles.Load(r.PathValue("id")); err != nil {
		fail(w, 404, "角色不存在")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var patch companion.Patch
	if !decodeInput(w, r, &patch) {
		return
	}
	snap, err := s.companion.Update(r.PathValue("id"), patch)
	if err != nil {
		companionFailure(w, err)
		return
	}
	writeJSON(w, 200, snap)
}
func (s *Server) companionContext(role string, mode string) (string, string, error) {
	snap, err := s.companion.Get(role)
	if err != nil {
		return "", "", err
	}
	policy, reference := companion.Context(snap, mode, role == "companion")
	return policy, reference, nil
}
