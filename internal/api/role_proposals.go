package api

import (
	"errors"
	"net/http"
	"strconv"

	"chat-agent/internal/roles"
)

func roleProposalFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, roles.ErrProposalNotFound):
		fail(w, 404, "角色或角色修改提案不存在")
	case errors.Is(err, roles.ErrProposalConflict):
		fail(w, 409, "提案版本或角色档案已变化，请刷新后重新提出修改")
	case errors.Is(err, roles.ErrProposalInvalid):
		fail(w, 400, "提案内容、操作或数量无效；请检查内容长度并清理旧提案")
	default:
		fail(w, 503, "角色修改提案暂时不可用；请刷新检查实际状态")
	}
}

func (s *Server) listRoleProposals(w http.ResponseWriter, r *http.Request) {
	items, err := s.roles.Proposals(r.PathValue("id"))
	if err != nil {
		roleProposalFailure(w, err)
		return
	}
	writeJSON(w, 200, items)
}

func (s *Server) reviewRoleProposal(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Revision int    `json:"revision"`
		Action   string `json:"action"`
	}
	if !decodeInput(w, r, &input) {
		return
	}
	p, err := s.roles.ReviewProposal(r.PathValue("id"), r.PathValue("proposal"), input.Action, input.Revision)
	if err != nil {
		roleProposalFailure(w, err)
		return
	}
	writeJSON(w, 200, p)
}

func (s *Server) deleteRoleProposal(w http.ResponseWriter, r *http.Request) {
	revision, err := strconv.Atoi(r.URL.Query().Get("revision"))
	if err != nil || revision < 1 {
		roleProposalFailure(w, roles.ErrProposalInvalid)
		return
	}
	if err := s.roles.DeleteProposal(r.PathValue("id"), r.PathValue("proposal"), revision); err != nil {
		roleProposalFailure(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
