// Package memory exposes approved long-term memory as a tool.
//
// It is the first plugin that needs a role: memory is scoped per role, and until
// probe.ToolRole existed a plugin could only learn which conversation it was in, not
// whose memory to search. That was the reason tools like this one stayed in the API
// package, where the server object already knew the role.
package memory

import (
	"context"
	"encoding/json"
	"errors"

	"chat-agent/internal/memory"
	"chat-agent/internal/pluginapi"
	"chat-agent/internal/probe"
)

const PluginID = "builtin.memory"

// Searcher is the narrow slice of the memory store this plugin uses.
type Searcher interface {
	SearchContext(ctx context.Context, role, query string, limit int) ([]memory.Entry, error)
}

// New returns nil when no memory store is configured, matching the other builtins.
func New(store Searcher) pluginapi.Plugin {
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
				Name:        "记忆检索",
				Version:     "0.1.0",
				Description: "按角色检索已批准的长期记忆",
			},
			Spec: pluginapi.Spec{
				Runtime:      pluginapi.RuntimeBuiltin,
				Capabilities: []string{"memory.search"},
				Tools:        []string{"memory_search"},
			},
		},
	}
}

// Plugin answers "what does this role already know".
type Plugin struct {
	store    Searcher
	manifest pluginapi.Manifest
}

func (p *Plugin) Manifest() pluginapi.Manifest                { return p.manifest }
func (p *Plugin) Start(context.Context, pluginapi.Host) error { return nil }
func (p *Plugin) Stop(context.Context) error                  { return nil }

func (p *Plugin) Register(registry *pluginapi.Registry) error {
	return registry.Tool(pluginapi.Tool{
		Name:  "memory_search",
		Order: 300,
		Usage: "look up this role's approved facts before asking the user to repeat something they already told you.",
		Description: "Search only approved long-term memory for this role. " +
			"Empty query returns recent approved facts.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`),
		Handler: func(ctx context.Context, data json.RawMessage) (pluginapi.Result, error) {
			var args struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(data, &args); err != nil {
				return pluginapi.Result{}, err
			}
			role := probe.ToolRole(ctx)
			if role == "" {
				return pluginapi.Result{}, errors.New("工具上下文缺少角色")
			}
			entries, err := p.store.SearchContext(ctx, role, args.Query, 5)
			if err != nil {
				return pluginapi.Result{}, errors.New("记忆查询失败")
			}
			out, err := json.Marshal(entries)
			if err != nil {
				return pluginapi.Result{}, err
			}
			return pluginapi.Result{Output: string(out)}, nil
		},
	})
}
