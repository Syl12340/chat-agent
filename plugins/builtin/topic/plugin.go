// Package topic exposes this role's optional public-interest discovery settings
// as a tool.
//
// It is a sibling of the care plugin and exists partly to prove the two seams
// care needed are general: probe.ToolInputs supplies the current turn's user
// text for the evidence check, and pluginapi.WriteGate serializes a revisioned
// write against other live conversations of the same role.
package topic

import (
	"context"
	"encoding/json"
	"errors"

	"chat-agent/internal/pluginapi"
	"chat-agent/internal/probe"
	"chat-agent/internal/topics"
)

const PluginID = "builtin.topic"

// Store is the narrow slice of the topic store this tool uses.
type Store interface {
	Settings(role string) (topics.Settings, error)
	UpdateSettings(role string, p topics.Settings) (topics.Settings, error)
}

// Source reports whether the deployment can actually search and read the public
// web. Enabling discovery without both is a promise the host cannot keep, so the
// tool refuses instead of scheduling work that will always fail.
type Source interface {
	SearchEnabled() bool
	FetchEnabled() bool
}

// New returns nil when any dependency is missing, matching the other builtins.
func New(store Store, source Source, gate pluginapi.WriteGate) pluginapi.Plugin {
	if store == nil || source == nil || gate == nil {
		return nil
	}
	return &Plugin{
		store:  store,
		source: source,
		gate:   gate,
		manifest: pluginapi.Manifest{
			APIVersion: pluginapi.APIVersion,
			Kind:       pluginapi.Kind,
			Metadata: pluginapi.Metadata{
				ID:          PluginID,
				Name:        "兴趣发现",
				Version:     "0.1.0",
				Description: "按角色读取和调整可选的公开兴趣发现设置",
			},
			Spec: pluginapi.Spec{
				Runtime:      pluginapi.RuntimeBuiltin,
				Capabilities: []string{"topic.settings"},
				Tools:        []string{"topic_preferences"},
			},
		},
	}
}

// Plugin answers "may this role look outside the conversation on its own".
type Plugin struct {
	store    Store
	source   Source
	gate     pluginapi.WriteGate
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
		Name:  "topic_preferences",
		Order: 810,
		Usage: "read current settings first; change public-interest discovery only on the user's explicit lasting request. Queries are sent to public search engines, so never include secrets or private personal context.",
		Description: "Read or update this role's optional public-interest topic discovery. It defaults off. " +
			"Explicit user opt-in is required before enabling background search; ordinary interests are not permission. " +
			"Queries are short public subjects (max 5, each 80 characters), not private memories. " +
			"Same-day time window, 1..2 daily slots, 0..100 percent daily scan probability and 1..3 per-role limit; " +
			"global cap 3 attempts, 3h spacing and unread restraint remain enforced. Reads never trigger work. " +
			"Writes require current revision and an exact current-turn user quote. " +
			"This is not phone push or a general autonomous agent.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["read","update"]},"revision":{"type":"integer","minimum":0},"evidence":{"type":"string","maxLength":8192},"enabled":{"type":"boolean"},"queries":{"type":"array","maxItems":5,"items":{"type":"string","minLength":1,"maxLength":80}},"window_start":{"type":"string"},"window_end":{"type":"string"},"visits_per_day":{"type":"integer","minimum":1,"maximum":2},"scan_probability":{"type":"integer","minimum":0,"maximum":100},"daily_limit":{"type":"integer","minimum":1,"maximum":3}},"required":["action"],"additionalProperties":false}`),
		Handler:    p.handle,
	}
}

func (p *Plugin) handle(ctx context.Context, raw json.RawMessage) (pluginapi.Result, error) {
	var args struct {
		Action      string    `json:"action"`
		Revision    int       `json:"revision"`
		Evidence    string    `json:"evidence"`
		Enabled     *bool     `json:"enabled"`
		Queries     *[]string `json:"queries"`
		WindowStart *string   `json:"window_start"`
		WindowEnd   *string   `json:"window_end"`
		Visits      *int      `json:"visits_per_day"`
		Probability *int      `json:"scan_probability"`
		Limit       *int      `json:"daily_limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return pluginapi.Result{}, err
	}
	role := probe.ToolRole(ctx)
	session := probe.ToolSession(ctx)
	if role == "" || session == "" {
		return pluginapi.Result{}, errors.New("工具上下文缺少角色或会话")
	}
	p0, err := p.store.Settings(role)
	if err != nil {
		return pluginapi.Result{}, err
	}
	changed := args.Enabled != nil || args.Queries != nil || args.WindowStart != nil || args.WindowEnd != nil || args.Visits != nil || args.Probability != nil || args.Limit != nil
	if args.Action == "read" {
		if changed {
			return pluginapi.Result{}, errors.New("read cannot also update settings")
		}
		return encode(p0)
	}
	if args.Action != "update" || !changed || !probe.TurnHasEvidence(probe.ToolInputs(ctx), args.Evidence) {
		return pluginapi.Result{}, errors.New("topic settings need a real current user request and changed fields")
	}
	p0.Revision = args.Revision
	if args.Enabled != nil {
		p0.Enabled = *args.Enabled
	}
	if args.Queries != nil {
		p0.Queries = *args.Queries
	}
	if args.WindowStart != nil {
		p0.WindowStart = *args.WindowStart
	}
	if args.WindowEnd != nil {
		p0.WindowEnd = *args.WindowEnd
	}
	if args.Visits != nil {
		p0.VisitsPerDay = *args.Visits
	}
	if args.Probability != nil {
		p0.ScanProbability = *args.Probability
	}
	if args.Limit != nil {
		p0.DailyLimit = *args.Limit
	}
	if p0.Enabled && (!p.source.SearchEnabled() || !p.source.FetchEnabled()) {
		return pluginapi.Result{}, errors.New("public search and fetch must be configured first")
	}
	if err := ctx.Err(); err != nil {
		return pluginapi.Result{}, err
	}
	saved := p0
	err = p.gate.WithRoleWrite(role, session, func() error {
		next, err := p.store.UpdateSettings(role, saved)
		if err != nil {
			return err
		}
		saved = next
		return nil
	})
	switch {
	case errors.Is(err, pluginapi.ErrWriteSessionEnded):
		return pluginapi.Result{}, errors.New("对话已停止，没有修改兴趣发现设置")
	case errors.Is(err, pluginapi.ErrWriteRoleBusy):
		return pluginapi.Result{}, errors.New("同角色其他对话正在运行，请稍后再调整兴趣发现")
	case err != nil:
		return pluginapi.Result{}, err
	}
	return encode(saved)
}

func encode(value any) (pluginapi.Result, error) {
	out, err := json.Marshal(value)
	if err != nil {
		return pluginapi.Result{}, err
	}
	return pluginapi.Result{Output: string(out)}, nil
}
