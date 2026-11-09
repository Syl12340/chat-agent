package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"chat-agent/internal/probe"
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

// The model can only create a pending PROFILE proposal. Review and application
// are deliberately absent from its tool surface and belong to authenticated UI.
func (s *Server) roleProposalTools(role roles.Role, inputs []string, sessionID string) []probe.Tool {
	baseHash := roles.ProfileHash(role.Profile)
	return []probe.Tool{{Name: "role_change_propose", Order: 520,
		Usage:       "propose a lasting PROFILE.md change only when requested; the user must separately review and approve it before it takes effect.",
		Description: "Create only a pending proposal to replace the current role's PROFILE.md. Use only when the current user requests a lasting role profile change. Copy a short exact quote from a current-turn user message as evidence. content is the complete proposed PROFILE.md, preserving all unrelated existing text, at most 16384 UTF-8 bytes; reason explains the requested change in at most 1024 bytes. Never propose temporary style, inferred personality, tool permissions, identity or safety/rule changes; RULES.md and IDENTITY.md cannot be changed here. The proposal does not affect this or future conversations until the user reviews and approves it in the role panel. A successful result means pending review only; never claim the role has already changed. If the profile changed since this turn started, ask the user to retry from the updated profile.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"content":{"type":"string","minLength":1,"maxLength":16384},"reason":{"type":"string","minLength":1,"maxLength":1024},"evidence":{"type":"string","minLength":1,"maxLength":8192}},"required":["content","reason","evidence"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, data json.RawMessage) (probe.ToolResult, error) {
			if err := ctx.Err(); err != nil {
				return probe.ToolResult{}, err
			}
			var args struct {
				Content  string `json:"content"`
				Reason   string `json:"reason"`
				Evidence string `json:"evidence"`
			}
			if err := toolInput(data, &args); err != nil {
				return probe.ToolResult{}, err
			}
			if !turnHasEvidence(inputs, args.Evidence) {
				return probe.ToolResult{}, errors.New("角色修改提案缺少当前用户消息中的依据")
			}
			if err := ctx.Err(); err != nil {
				return probe.ToolResult{}, err
			}
			p, err := s.roles.ProposeProfile(role.ID, baseHash, args.Content, args.Reason, sessionID)
			if errors.Is(err, roles.ErrProposalConflict) {
				return probe.ToolResult{}, errors.New("角色档案已更新；未创建提案，请基于新档案重试")
			}
			if err != nil {
				return probe.ToolResult{}, errors.New("角色修改提案保存失败，请检查内容长度或旧提案数量")
			}
			out, _ := json.Marshal(map[string]any{"id": p.ID, "role_id": p.RoleID, "file": p.File, "content": p.Content, "reason": p.Reason, "status": p.Status, "revision": p.Revision, "source_session_id": p.SourceSessionID, "created_at": p.CreatedAt, "requires_user_review": true})
			return probe.ToolResult{Output: string(out)}, nil
		},
	}}
}
