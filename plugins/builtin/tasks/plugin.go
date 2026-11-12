// Package tasks exposes the role's saved todos and reminders for reading.
//
// Only the list side lives here so far. Adding an item carries an idempotency key and
// an atomic business receipt (tasks.AddWithReceipt), which is a larger move; reading is
// plain store access and makes a good first step for the same reason memory_search
// did.
package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"chat-agent/internal/pluginapi"
	"chat-agent/internal/probe"
	"chat-agent/internal/tasks"
)

const PluginID = "builtin.tasks"

// Store is the narrow slice of the task store this plugin uses.
type Store interface {
	List(role, kind string) ([]tasks.Item, error)
	Update(role, id, status string, revision int, now time.Time) (tasks.Item, error)
}

// New returns nil when no task store is configured, matching the other builtins.
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
				Name:        "待办与提醒",
				Version:     "0.1.0",
				Description: "列出、创建当前角色的待办与提醒",
			},
			Spec: pluginapi.Spec{
				Runtime:      pluginapi.RuntimeBuiltin,
				Capabilities: []string{"tasks.read"},
				Tools:        []string{"todo_list", "todo_complete", "reminder_list", "reminder_cancel"},
			},
		},
	}
}

// Plugin lists saved items.
type Plugin struct {
	store    Store
	manifest pluginapi.Manifest
}

func (p *Plugin) Manifest() pluginapi.Manifest                { return p.manifest }
func (p *Plugin) Start(context.Context, pluginapi.Host) error { return nil }
func (p *Plugin) Stop(context.Context) error                  { return nil }

func (p *Plugin) Register(registry *pluginapi.Registry) error {
	for _, spec := range []struct {
		list, finish, kind, status string
	}{
		{"todo_list", "todo_complete", "todo", "done"},
		{"reminder_list", "reminder_cancel", "reminder", "canceled"},
	} {
		if err := registry.Tool(p.listTool(spec.list, spec.kind)); err != nil {
			return err
		}
		if err := registry.Tool(p.finishTool(spec.finish, spec.kind, spec.status)); err != nil {
			return err
		}
	}
	return nil
}

// finishTool closes an item, or reopens a done todo. It needs the latest revision so
// two callers cannot silently overwrite each other.
func (p *Plugin) finishTool(name, kind, status string) pluginapi.Tool {
	return pluginapi.Tool{
		Name:  name,
		Order: 720,
		Usage: "change only the requested item using its latest ID and revision.",
		Description: "Set the current role's " + kind + " to " + status +
			" only when explicitly requested. Read its ID and current revision from the list first.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"},"revision":{"type":"integer"}},"required":["id","revision"],"additionalProperties":false}`),
		Handler: func(ctx context.Context, data json.RawMessage) (pluginapi.Result, error) {
			var args struct {
				ID       string `json:"id"`
				Revision int    `json:"revision"`
			}
			if err := json.Unmarshal(data, &args); err != nil {
				return pluginapi.Result{}, err
			}
			role := probe.ToolRole(ctx)
			if role == "" {
				return pluginapi.Result{}, errors.New("工具上下文缺少角色")
			}
			items, err := p.store.List(role, kind)
			if err != nil {
				return pluginapi.Result{}, errors.New("任务读取失败")
			}
			for _, item := range items {
				if item.ID != args.ID {
					continue
				}
				updated, err := p.store.Update(role, args.ID, status, args.Revision, time.Now())
				if err != nil {
					return pluginapi.Result{}, errors.New("记录已变化，请重新读取后操作")
				}
				out, err := json.Marshal(updated)
				if err != nil {
					return pluginapi.Result{}, err
				}
				return pluginapi.Result{Output: string(out)}, nil
			}
			return pluginapi.Result{}, errors.New("当前角色没有此任务")
		},
	}
}

func (p *Plugin) listTool(name, kind string) pluginapi.Tool {
	return pluginapi.Tool{
		Name:  name,
		Order: 700,
		Usage: "read actual IDs and revisions before changing a saved item.",
		Description: "List only the current role's " + kind +
			" items, including IDs, states and revisions.",
		Parameters: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Handler: func(ctx context.Context, _ json.RawMessage) (pluginapi.Result, error) {
			role := probe.ToolRole(ctx)
			if role == "" {
				return pluginapi.Result{}, errors.New("工具上下文缺少角色")
			}
			items, err := p.store.List(role, kind)
			if err != nil {
				return pluginapi.Result{}, errors.New("任务读取失败")
			}
			truncated := len(items) > 50
			if truncated {
				items = items[:50]
			}
			out, err := json.Marshal(struct {
				Items     []tasks.Item `json:"items"`
				Truncated bool         `json:"truncated"`
			}{items, truncated})
			if err != nil {
				return pluginapi.Result{}, err
			}
			return pluginapi.Result{Output: string(out)}, nil
		},
	}
}
