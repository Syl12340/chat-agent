// Package preferences exposes the ongoing communication preferences as a tool.
//
// It was the last host tool that only needed the current turn's user text to
// check an evidence quote, so probe.ToolInputs is enough to move it out. The store
// enforces the revision itself (`companion.Patch` carries it), which is why this
// tool needs no write gate: two conversations cannot both land a stale revision.
package preferences

import (
	"context"
	"encoding/json"
	"errors"

	"chat-agent/internal/companion"
	"chat-agent/internal/pluginapi"
	"chat-agent/internal/probe"
)

const PluginID = "builtin.preferences"

// Store is the narrow slice of the companion store this tool uses.
type Store interface {
	Get(role string) (companion.Snapshot, error)
	Update(role string, patch companion.Patch) (companion.Snapshot, error)
}

// New returns nil when no companion store is configured.
func New(store Store) pluginapi.Plugin {
	if store == nil {
		return nil
	}
	return &Plugin{
		store: store,
		manifest: pluginapi.Manifest{
			APIVersion: pluginapi.APIVersion,
			Kind:       pluginapi.Kind,
			Metadata: pluginapi.Metadata{
				ID:          PluginID,
				Name:        "交流偏好",
				Version:     "0.1.0",
				Description: "按角色保存用户明确要求的长期交流方式",
			},
			Spec: pluginapi.Spec{
				Runtime:      pluginapi.RuntimeBuiltin,
				Capabilities: []string{"companion.preferences"},
				Tools:        []string{"companion_preferences_update"},
			},
		},
	}
}

// Plugin answers "how does this person want to be talked to".
type Plugin struct {
	store    Store
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
		Name:  "companion_preferences_update",
		Order: 500,
		Usage: "save an ongoing communication preference, not a one-off request.",
		Description: "Save only the ongoing communication preferences explicitly requested in the latest user " +
			"message. Copy a short exact quote from a current-turn user message into evidence. Interpret natural " +
			"language and context, without command syntax. Include current preference revision. Omit every " +
			"unrequested field; never save temporary style, feelings, personal facts or content quoted from other " +
			"sources. Successful output contains the new saved preferences; only then acknowledge saving.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"revision":{"type":"integer","minimum":0},"evidence":{"type":"string","maxLength":1024},"preferred_name":{"type":"string","maxLength":40},"reply_length":{"type":"string","enum":["default","short","detailed"]},"tone":{"type":"string","enum":["default","natural","warm","playful"]},"advice":{"type":"string","enum":["default","ask_first","on_request"]},"questions":{"type":"string","enum":["default","light","normal"]}},"required":["revision","evidence"],"additionalProperties":false}`),
		Handler:    p.handle,
	}
}

func (p *Plugin) handle(ctx context.Context, data json.RawMessage) (pluginapi.Result, error) {
	var args struct {
		companion.Patch
		Evidence string `json:"evidence"`
	}
	if err := json.Unmarshal(data, &args); err != nil {
		return pluginapi.Result{}, err
	}
	role := probe.ToolRole(ctx)
	if role == "" {
		return pluginapi.Result{}, errors.New("工具上下文缺少角色")
	}
	if !probe.TurnHasEvidence(probe.ToolInputs(ctx), args.Evidence) {
		return pluginapi.Result{}, errors.New("偏好修改缺少当前用户消息中的依据")
	}
	current, err := p.store.Get(role)
	if err != nil {
		return pluginapi.Result{}, errors.New("陪伴偏好暂时不可用")
	}
	if args.Revision != current.Revision {
		return pluginapi.Result{}, errors.New("偏好版本不匹配，请读取当前设置后重试")
	}
	saved, err := p.store.Update(role, args.Patch)
	if errors.Is(err, companion.ErrConflict) {
		return pluginapi.Result{}, errors.New("偏好已在别处修改，请重新打开设置")
	}
	if err != nil {
		return pluginapi.Result{}, errors.New("偏好保存失败，请检查字段或使用陪伴偏好设置")
	}
	out, err := json.Marshal(saved)
	if err != nil {
		return pluginapi.Result{}, err
	}
	return pluginapi.Result{Output: string(out)}, nil
}
