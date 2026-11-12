// Package tasks exposes the role's saved todos and reminders for reading.
//
// Only the list side lives here so far. Adding an item carries an idempotency key and
// an atomic business receipt (tasks.AddWithReceipt), which is a larger move; reading is
// plain store access and makes a good first step for the same reason memory_search
// did.
package tasks

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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
	Add(role, kind, title, key, source, session string, due *time.Time, now time.Time) (tasks.Item, error)
	AddWithReceipt(role, kind, title, operationID, argumentsSHA256, operationKind, source, session string, due *time.Time, now time.Time) (tasks.Item, tasks.Receipt, bool, error)
}

// New returns nil when no task store is configured, matching the other builtins.
func New(store Store) pluginapi.Plugin {
	if store == nil {
		return nil
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil
	}
	return &Plugin{
		store: store,
		nonce: hex.EncodeToString(nonce[:]),
		manifest: pluginapi.Manifest{
			APIVersion: pluginapi.APIVersion,
			Kind:       pluginapi.Kind,
			Metadata: pluginapi.Metadata{
				ID:          PluginID,
				Name:        "待办与提醒",
				Version:     "0.1.0",
				Description: "创建、列出和关闭当前角色的待办与提醒",
			},
			Spec: pluginapi.Spec{
				Runtime:      pluginapi.RuntimeBuiltin,
				Capabilities: []string{"tasks.read"},
				Tools:        []string{"todo_add", "todo_list", "todo_complete", "reminder_add", "reminder_list", "reminder_cancel"},
			},
		},
	}
}

// Plugin lists saved items.
type Plugin struct {
	store    Store
	nonce    string
	manifest pluginapi.Manifest
}

func (p *Plugin) Manifest() pluginapi.Manifest                { return p.manifest }
func (p *Plugin) Start(context.Context, pluginapi.Host) error { return nil }
func (p *Plugin) Stop(context.Context) error                  { return nil }

func (p *Plugin) Register(registry *pluginapi.Registry) error {
	for _, spec := range []struct {
		add, list, finish, kind, status string
	}{
		{"todo_add", "todo_list", "todo_complete", "todo", "done"},
		{"reminder_add", "reminder_list", "reminder_cancel", "reminder", "canceled"},
	} {
		if err := registry.Tool(p.addTool(spec.add, spec.kind)); err != nil {
			return err
		}
		if err := registry.Tool(p.listTool(spec.list, spec.kind)); err != nil {
			return err
		}
		if err := registry.Tool(p.finishTool(spec.finish, spec.kind, spec.status)); err != nil {
			return err
		}
	}
	return nil
}

// addTool creates one item. When the host supplies an operation id the write goes
// through AddWithReceipt so a retried call replays instead of duplicating, and the
// business receipt travels back with the result the same way it did from the host.
func (p *Plugin) addTool(name, kind string) pluginapi.Tool {
	schema := `{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}`
	if kind == "reminder" {
		schema = `{"type":"object","properties":{"title":{"type":"string"},"due_at":{"type":"string","description":"RFC3339 timestamp with an explicit time-zone offset; use the configured user timezone."}},"required":["title","due_at"],"additionalProperties":false}`
	}
	return pluginapi.Tool{
		Name:    name,
		Order:   710,
		Receipt: string(probe.ReceiptAtomic),
		Usage:   "create only the requested item; reminder timestamps require an explicit timezone offset and deliver to the inbox.",
		Description: "Create a role-scoped " + kind + " only when the user asks. " +
			"Reminders are one-shot inbox items, not closed-app push notifications. " +
			"Identical active items are deduplicated.",
		Parameters: json.RawMessage(schema),
		Handler: func(ctx context.Context, data json.RawMessage) (pluginapi.Result, error) {
			var args struct {
				Title string     `json:"title"`
				DueAt *time.Time `json:"due_at"`
			}
			if err := json.Unmarshal(data, &args); err != nil {
				return pluginapi.Result{}, err
			}
			role := probe.ToolRole(ctx)
			if role == "" {
				return pluginapi.Result{}, errors.New("工具上下文缺少角色")
			}
			session := probe.ToolSession(ctx)
			now := time.Now()

			if operationID := probe.ToolOperationID(ctx); operationID != "" {
				digest := sha256.Sum256(data)
				item, receipt, replayed, err := p.store.AddWithReceipt(
					role, kind, args.Title, operationID, hex.EncodeToString(digest[:]),
					name, "agent", session, args.DueAt, now)
				if err != nil {
					return pluginapi.Result{}, errors.New("任务创建失败，请检查标题或带时区的提醒时间")
				}
				var current *tasks.Item
				if item.ID != "" {
					current = &item
				}
				out, err := json.Marshal(struct {
					Committed bool          `json:"committed"`
					Replayed  bool          `json:"replayed"`
					Item      *tasks.Item   `json:"item,omitempty"`
					Receipt   tasks.Receipt `json:"receipt"`
				}{true, replayed, current, receipt})
				if err != nil {
					return pluginapi.Result{}, err
				}
				return pluginapi.Result{
					Output: string(out),
					Receipt: &pluginapi.Receipt{
						Store:              "tasks",
						OperationID:        receipt.OperationID,
						ArgumentsSHA256:    receipt.ArgumentsSHA256,
						EntityIDs:          append([]string(nil), receipt.EntityIDs...),
						ResultingRevisions: append([]int(nil), receipt.ResultingRevisions...),
						OutcomeDigest:      receipt.OutcomeDigest,
					},
				}, nil
			}

			// No operation id: derive a per-call key so a duplicate submit still collapses.
			key := sha256.Sum256([]byte(p.nonce + "\x00" + probe.ToolCallID(ctx)))
			item, err := p.store.Add(role, kind, args.Title, hex.EncodeToString(key[:]), "agent", session, args.DueAt, now)
			if err != nil {
				return pluginapi.Result{}, errors.New("任务创建失败，请检查标题或带时区的提醒时间")
			}
			out, err := json.Marshal(item)
			if err != nil {
				return pluginapi.Result{}, err
			}
			return pluginapi.Result{Output: string(out)}, nil
		},
	}
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
