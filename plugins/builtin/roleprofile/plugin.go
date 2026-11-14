// Package roleprofile exposes "propose a lasting change to this role's profile"
// as a tool.
//
// The model can only create a pending proposal. Applying one stays in the
// authenticated panel: review and application are deliberately absent from the
// tool surface, so crossing that line would be worse than the tool being absent.
package roleprofile

import (
	"context"
	"encoding/json"
	"errors"

	"chat-agent/internal/pluginapi"
	"chat-agent/internal/probe"
	"chat-agent/internal/roles"
)

const PluginID = "builtin.roleprofile"

// Profiles is the narrow slice of the role manager this tool uses: it reads the
// current profile so a proposal is bound to the version it was written against.
type Profiles interface {
	Load(id string) (roles.Role, error)
	ProposeProfile(role, baseHash, content, reason, sessionID string) (roles.Proposal, error)
}

// New returns nil when no role manager is configured.
func New(profiles Profiles) pluginapi.Plugin {
	if profiles == nil {
		return nil
	}
	return &Plugin{
		profiles: profiles,
		manifest: pluginapi.Manifest{
			APIVersion: pluginapi.APIVersion,
			Kind:       pluginapi.Kind,
			Metadata: pluginapi.Metadata{
				ID:          PluginID,
				Name:        "角色档案",
				Version:     "0.1.0",
				Description: "按用户要求提交待审核的角色档案修改建议",
			},
			Spec: pluginapi.Spec{
				Runtime:      pluginapi.RuntimeBuiltin,
				Capabilities: []string{"role.profile"},
				Tools:        []string{"role_change_propose"},
			},
		},
	}
}

// Plugin answers "should this persona change".
type Plugin struct {
	profiles Profiles
	manifest pluginapi.Manifest
}

func (p *Plugin) Manifest() pluginapi.Manifest                { return p.manifest }
func (p *Plugin) Start(context.Context, pluginapi.Host) error { return nil }
func (p *Plugin) Stop(context.Context) error                  { return nil }

func (p *Plugin) Register(registry *pluginapi.Registry) error {
	return registry.Tool(p.tool())
}

func (p *Plugin) tool() pluginapi.Tool {
	return pluginapi.Tool{
		Name:  "role_change_propose",
		Order: 520,
		Usage: "propose a lasting PROFILE.md change only when requested; the user must separately review and approve it before it takes effect.",
		Description: "Create only a pending proposal to replace the current role's PROFILE.md. Use only when the " +
			"current user requests a lasting role profile change. Copy a short exact quote from a current-turn user " +
			"message as evidence. content is the complete proposed PROFILE.md, preserving all unrelated existing " +
			"text, at most 16384 UTF-8 bytes; reason explains the requested change in at most 1024 bytes. Never " +
			"propose temporary style, inferred personality, tool permissions, identity or safety/rule changes; " +
			"RULES.md and IDENTITY.md cannot be changed here. The proposal does not affect this or future " +
			"conversations until the user reviews and approves it in the role panel. A successful result means " +
			"pending review only; never claim the role has already changed. If the profile changed since this turn " +
			"started, ask the user to retry from the updated profile.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"content":{"type":"string","minLength":1,"maxLength":16384},"reason":{"type":"string","minLength":1,"maxLength":1024},"evidence":{"type":"string","minLength":1,"maxLength":8192}},"required":["content","reason","evidence"],"additionalProperties":false}`),
		Handler:    p.handle,
	}
}

func (p *Plugin) handle(ctx context.Context, data json.RawMessage) (pluginapi.Result, error) {
	var args struct {
		Content  string `json:"content"`
		Reason   string `json:"reason"`
		Evidence string `json:"evidence"`
	}
	if err := json.Unmarshal(data, &args); err != nil {
		return pluginapi.Result{}, err
	}
	role := probe.ToolRole(ctx)
	session := probe.ToolSession(ctx)
	if role == "" || session == "" {
		return pluginapi.Result{}, errors.New("工具上下文缺少角色或会话")
	}
	if !probe.TurnHasEvidence(probe.ToolInputs(ctx), args.Evidence) {
		return pluginapi.Result{}, errors.New("角色修改提案缺少当前用户消息中的依据")
	}
	// The host hashed the profile when it assembled this turn's tools. Here the
	// profile is read at call time, which is one step weaker: a change landing
	// between assembly and this call would not be noticed. Applying a proposal
	// needs an authenticated review, so that window cannot produce a silent
	// change — the proposal is still pending either way.
	current, err := p.profiles.Load(role)
	if err != nil {
		return pluginapi.Result{}, errors.New("角色档案暂时不可用")
	}
	if err := ctx.Err(); err != nil {
		return pluginapi.Result{}, err
	}
	proposal, err := p.profiles.ProposeProfile(role, roles.ProfileHash(current.Profile), args.Content, args.Reason, session)
	if errors.Is(err, roles.ErrProposalConflict) {
		return pluginapi.Result{}, errors.New("角色档案已更新；未创建提案，请基于新档案重试")
	}
	if err != nil {
		return pluginapi.Result{}, errors.New("角色修改提案保存失败，请检查内容长度或旧提案数量")
	}
	out, err := json.Marshal(map[string]any{"id": proposal.ID, "role_id": proposal.RoleID, "file": proposal.File, "content": proposal.Content, "reason": proposal.Reason, "status": proposal.Status, "revision": proposal.Revision, "source_session_id": proposal.SourceSessionID, "created_at": proposal.CreatedAt, "requires_user_review": true})
	if err != nil {
		return pluginapi.Result{}, err
	}
	return pluginapi.Result{Output: string(out)}, nil
}
