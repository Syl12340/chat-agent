package api

import (
	"errors"
	"net/http"

	"pocket-agent/internal/probe"
	"pocket-agent/internal/turns"
)

// deleteSession removes a conversation and everything the server keeps for it:
// the chat journal, its lock and the turn ledger. Role-scoped memory, events and
// activities are intentionally kept — they belong to the role, not the session.
func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
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
	if err := probe.DeleteSession(s.config.DataDir, probe.AgentID, id); err != nil {
		fail(w, 500, "无法删除会话记录")
		return
	}
	if err := s.turns.Delete(id); err != nil {
		fail(w, 500, "会话已删除，但待处理记录未能清理")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// purgeTurn physically removes one conversation turn: its messages leave the
// journal file and its record leaves the ledger. Only the newest turn can be
// purged, because earlier turns would leave later indexes pointing at messages
// that no longer exist.
func (s *Server) purgeTurn(w http.ResponseWriter, r *http.Request, sess *probe.Session) {
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
	dropped, err := sess.DropTail(total - turn.StartIndex)
	if err != nil {
		if errors.Is(err, probe.ErrSessionBusy) {
			fail(w, 409, "会话正在生成，请先停止")
			return
		}
		fail(w, 503, "无法删除这条记录")
		return
	}
	if err := s.turns.Remove(id, rid); err != nil && !errors.Is(err, turns.ErrNotFound) {
		fail(w, 500, "聊天记录已删除，但队列记录未能清理")
		return
	}
	writeJSON(w, 200, map[string]int{"removed_messages": dropped})
}
