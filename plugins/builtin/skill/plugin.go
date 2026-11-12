// Package skill exposes role skills as a tool.
//
// A skill is a Markdown file in the role workspace. Reading one grants nothing — the
// description says so explicitly — which is why it is safe to serve from a plugin like
// the other read-only tools.
package skill

import (
	"context"
	"encoding/json"
	"errors"

	"chat-agent/internal/pluginapi"
	"chat-agent/internal/probe"
	"chat-agent/internal/roles"
)

const PluginID = "builtin.skill"

// Loader is the narrow slice of the role manager this plugin needs.
type Loader interface {
	Load(id string) (roles.Role, error)
}

// New returns nil when no role manager is configured, matching the other builtins.
func New(loader Loader) pluginapi.Plugin {
	if loader == nil {
		return nil
	}
	return &Plugin{
		loader: loader,
		manifest: pluginapi.Manifest{
			APIVersion: pluginapi.APIVersion,
			Kind:       pluginapi.Kind,
			Metadata: pluginapi.Metadata{
				ID:          PluginID,
				Name:        "角色技能",
				Version:     "0.1.0",
				Description: "读取当前角色工作区里的 Markdown 技能",
			},
			Spec: pluginapi.Spec{
				Runtime:      pluginapi.RuntimeBuiltin,
				Capabilities: []string{"role.skill"},
				Tools:        []string{"skill_load"},
			},
		},
	}
}

// Plugin serves skill bodies.
type Plugin struct {
	loader   Loader
	manifest pluginapi.Manifest
}

func (p *Plugin) Manifest() pluginapi.Manifest                { return p.manifest }
func (p *Plugin) Start(context.Context, pluginapi.Host) error { return nil }
func (p *Plugin) Stop(context.Context) error                  { return nil }

func (p *Plugin) Register(registry *pluginapi.Registry) error {
	return registry.Tool(pluginapi.Tool{
		Name:  "skill_load",
		Order: 400,
		Usage: "read a role skill when its workflow would genuinely help this turn; it grants no extra permissions.",
		Description: "Read a Markdown skill from the current role workspace. " +
			"Does not execute code or grant permissions.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"],"additionalProperties":false}`),
		Handler: func(ctx context.Context, data json.RawMessage) (pluginapi.Result, error) {
			var args struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(data, &args); err != nil {
				return pluginapi.Result{}, err
			}
			roleID := probe.ToolRole(ctx)
			if roleID == "" {
				return pluginapi.Result{}, errors.New("工具上下文缺少角色")
			}
			role, err := p.loader.Load(roleID)
			if err != nil {
				return pluginapi.Result{}, errors.New("角色不可用")
			}
			for _, skill := range role.Skills {
				if skill.ID == args.Name {
					return pluginapi.Result{Output: skill.Body}, nil
				}
			}
			return pluginapi.Result{}, errors.New("当前角色没有此 Skill")
		},
	})
}
