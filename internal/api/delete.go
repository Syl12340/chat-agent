package api

import (
	"net/http"

	"pocket-agent/internal/probe"
)

// deleteSession removes a conversation and everything the server keeps for it:
// the chat journal, its lock and the turn ledger. Role-scoped memory, events and
// activities are intentionally kept — they belong to the role, not the session.
func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	s.mediaMu.Lock()
	defer s.mediaMu.Unlock()
	id := r.PathValue("id")
	if !validID(id) {
		fail(w, 400, "会话编号无效")
		return
	}
	s.mu.Lock()
	if s.runs[id] != nil {
		s.mu.Unlock()
		fail(w, 409, "会话正在生成，请先停止")
		return
	}
	sess := s.sessions[id]
	delete(s.sessions, id)
	s.mu.Unlock()

	if sess != nil {
		if err := sess.Close(); err != nil {
			fail(w, 503, "会话暂时无法关闭，请稍后重试")
			return
		}
	}
	deleteID, err := s.runtime.BeginSessionDeletion(id)
	if err != nil {
		fail(w, 503, "无法登记会话删除，请稍后重试")
		return
	}
	if err := s.images.DeleteSession(id); err != nil {
		writeJSON(w, 503, map[string]any{"error": "删除已登记，但图片尚未完全清理；请再次删除以继续", "delete_id": deleteID})
		return
	}
	s.turns.Forget(id)
	if err := s.runtime.RemoveSessionData(s.config.DataDir, id); err != nil {
		writeJSON(w, 503, map[string]any{"error": "删除已登记，但会话文件尚未完全清理；请再次删除以继续", "delete_id": deleteID})
		return
	}
	if err := s.runtime.CompleteDeletion(deleteID); err != nil {
		writeJSON(w, 503, map[string]any{"error": "会话内容已清理，但删除登记尚未结算；请再次删除以继续", "delete_id": deleteID})
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// purgeTurn physically removes one conversation turn: its messages leave the
// journal file and its record leaves the ledger. Only the newest turn can be
// purged, because earlier turns would leave later indexes pointing at messages
// that no longer exist.
func (s *Server) purgeTurn(w http.ResponseWriter, r *http.Request, sess *probe.Session) {
	s.mediaMu.Lock()
	defer s.mediaMu.Unlock()
	id, rid := r.PathValue("id"), r.PathValue("request")
	s.mu.Lock()
	running := s.runs[id] != nil
	s.mu.Unlock()
	if running {
		fail(w, 409, "会话正在生成，请先停止")
		return
	}
	turn, err := s.turns.Get(id, rid)
	if err != nil {
		turnFailure(w, err)
		return
	}
	total := len(sess.Messages())
	if turn.EndIndex != total || turn.StartIndex >= total {
		fail(w, 409, "只能删除最近的一条记录")
		return
	}
	removedImages := map[string]bool{}
	for _, message := range sess.Messages()[turn.StartIndex:] {
		for _, image := range message.Images {
			removedImages[image.ID] = true
		}
	}
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
	if err := sess.Close(); err != nil {
		fail(w, 503, "无法关闭会话以执行删除")
		return
	}
	s.turns.Forget(id)
	removedRequests, deleteID, err := s.runtime.RewriteLatestTurn(id, rid, turn.StartIndex)
	if err != nil {
		writeJSON(w, 503, map[string]any{"error": "删除已登记，但新存储代尚未完成；需要显式维护后重试", "delete_id": deleteID})
		return
	}
	historyPath, err := s.runtime.HistoryPath(id)
	if err != nil {
		fail(w, 503, "记录已删除，但新存储代暂时不可读")
		return
	}
	newSession, err := probe.OpenRuntimeSession(historyPath)
	if err != nil {
		fail(w, 503, "记录已删除，但会话暂时无法重开")
		return
	}
	s.mu.Lock()
	s.sessions[id] = newSession
	s.mu.Unlock()
	keep, err := s.referencedImages(newSession, id)
	if err != nil {
		fail(w, 500, "聊天记录已删除，但无法核对图片引用")
		return
	}
	// Preserve other uploaded, unsent drafts: delete only this turn's removed
	// references, after checking the complete retained journal and queue.
	for imageID := range removedImages {
		if !keep[imageID] {
			if err = s.images.Delete(newSession.RoleID(), id, imageID); err != nil {
				fail(w, 500, "聊天记录已删除，但图片清理未完成")
				return
			}
		}
	}
	writeJSON(w, 200, map[string]any{"removed_messages": total - turn.StartIndex, "removed_request_ids": removedRequests})
}
