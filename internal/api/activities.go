package api

import (
	"errors"
	"net/http"
	"os"
	"strconv"

	"pocket-agent/internal/companion"
)

func activityFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, companion.ErrActivityNotFound):
		fail(w, 404, "共同活动不存在")
	case errors.Is(err, companion.ErrConflict):
		fail(w, 409, "活动已变化、暂停或结束，请刷新后重试；创建标识不能用于另一项活动")
	case errors.Is(err, companion.ErrInvalid):
		fail(w, 400, "请检查活动类型、标题、进展与版本")
	case errors.Is(err, companion.ErrActivityLimit):
		fail(w, 429, "活动或保存标识数量已达上限，请整理已有活动")
	default:
		fail(w, 503, "共同活动暂时不可用，请稍后重试")
	}
}

func (s *Server) listActivities(w http.ResponseWriter, r *http.Request) {
	role, ok := s.memoryRole(w, r)
	if !ok {
		return
	}
	items, err := s.activities.List(role)
	if err != nil {
		activityFailure(w, err)
		return
	}
	writeJSON(w, 200, items)
}

func (s *Server) createActivity(w http.ResponseWriter, r *http.Request) {
	role, ok := s.memoryRole(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var draft companion.ActivityDraft
	if !decodeInput(w, r, &draft) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		fail(w, 503, "服务正在关闭")
		return
	}
	a, duplicate, err := s.activities.Create(role, draft)
	if err != nil {
		activityFailure(w, err)
		return
	}
	code := http.StatusCreated
	if duplicate {
		code = http.StatusOK
	}
	writeJSON(w, code, a)
}

func (s *Server) updateActivity(w http.ResponseWriter, r *http.Request) {
	role, ok := s.memoryRole(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var input struct {
		Revision int `json:"revision"`
		companion.ActivityPatch
	}
	if !decodeInput(w, r, &input) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		fail(w, 503, "服务正在关闭")
		return
	}
	if s.lifeRoleBusy(role) {
		fail(w, 409, "请先停止这个角色正在进行的对话，再修改活动")
		return
	}
	cuts, err := s.roleContextCuts(role)
	if err != nil {
		activityFailure(w, err)
		return
	}
	a, err := s.activities.Patch(role, r.PathValue("id"), input.Revision, input.ActivityPatch, cuts)
	if err != nil {
		activityFailure(w, err)
		return
	}
	writeJSON(w, 200, a)
}

func (s *Server) deleteActivity(w http.ResponseWriter, r *http.Request) {
	role, ok := s.memoryRole(w, r)
	if !ok {
		return
	}
	revision, err := strconv.Atoi(r.URL.Query().Get("revision"))
	if err != nil {
		activityFailure(w, companion.ErrInvalid)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		fail(w, 503, "服务正在关闭")
		return
	}
	if s.lifeRoleBusy(role) {
		fail(w, 409, "请先停止这个角色正在进行的对话，再删除活动")
		return
	}
	cuts, err := s.roleContextCuts(role)
	if err != nil {
		activityFailure(w, err)
		return
	}
	if err := s.activities.Delete(role, r.PathValue("id"), revision, cuts); err != nil {
		activityFailure(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) attachActivity(w http.ResponseWriter, r *http.Request) {
	role, ok := s.memoryRole(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var input struct {
		Revision  int    `json:"revision"`
		SessionID string `json:"session_id"`
	}
	if !decodeInput(w, r, &input) {
		return
	}
	if !validID(input.SessionID) {
		activityFailure(w, companion.ErrInvalid)
		return
	}
	sess, err := s.session(input.SessionID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fail(w, 404, "会话不存在")
		} else {
			activityFailure(w, err)
		}
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		fail(w, 503, "服务正在关闭")
		return
	}
	if sess.RoleID() != role {
		fail(w, 409, "活动与会话角色不同，请新建当前角色的会话")
		return
	}
	queued, _, err := s.turns.HasQueued(input.SessionID)
	if err != nil {
		activityFailure(w, err)
		return
	}
	if s.runs[input.SessionID] != nil || queued || len(sess.Messages()) != 0 {
		fail(w, 409, "请在没有待发送消息的新会话中开始活动")
		return
	}
	if err := s.activities.Bind(role, r.PathValue("id"), input.Revision, input.SessionID); err != nil {
		activityFailure(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
