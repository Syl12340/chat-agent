// Package episode exposes "remember this for later" as a tool.
//
// The host used to own it only because the write needs the current turn's user
// text to check an evidence quote. probe.ToolInputs supplies that, so the tool
// can live here where every other business capability now lives.
package episode

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"chat-agent/internal/companion"
	"chat-agent/internal/pluginapi"
	"chat-agent/internal/probe"
)

const PluginID = "builtin.episode"

// Store is the narrow slice of the life store this tool uses. AddEpisode always
// creates a pending candidate; nothing here can approve one.
type Store interface {
	AddEpisode(role string, draft companion.EpisodeDraft) (companion.Episode, error)
}

// New returns nil when no life store is configured, matching the other builtins.
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
				Name:        "事件候选",
				Version:     "0.1.0",
				Description: "按角色提出待确认的事件或未完话题",
			},
			Spec: pluginapi.Spec{
				Runtime:      pluginapi.RuntimeBuiltin,
				Capabilities: []string{"episode.propose"},
				Tools:        []string{"episode_propose"},
			},
		},
	}
}

// Plugin answers "what did the user ask to come back to".
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
		Name:     "episode_propose",
		Order:    330,
		Decision: string(probe.DecisionAdvisory),
		Risk:     string(probe.RiskLow),
		Usage:    "use only for a dated personal event or an unfinished topic the user asked to keep for later.",
		Description: "Propose a pending, role-scoped event or unfinished topic only when the current user " +
			"explicitly requests remembering or continuing it later. Copy a short exact quote from the latest " +
			"message into evidence. Never save credentials or diagnose emotional traits. Pending events are not " +
			"used across sessions until the user approves them in the Events panel. Followup requires an explicit " +
			"request to check in later and an RFC3339 time with timezone; otherwise omit it and set " +
			"allow_followup false.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string","maxLength":256},"content":{"type":"string","maxLength":4096},"evidence":{"type":"string","maxLength":1024},"event_at":{"type":["string","null"]},"followup_at":{"type":["string","null"]},"allow_followup":{"type":"boolean"}},"required":["title","content","evidence"],"additionalProperties":false}`),
		Handler:    p.handle,
	}
}

func (p *Plugin) handle(ctx context.Context, data json.RawMessage) (pluginapi.Result, error) {
	var args struct {
		Title         string     `json:"title"`
		Content       string     `json:"content"`
		Evidence      string     `json:"evidence"`
		EventAt       *time.Time `json:"event_at"`
		FollowupAt    *time.Time `json:"followup_at"`
		AllowFollowup bool       `json:"allow_followup"`
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
		return pluginapi.Result{}, errors.New("事件候选缺少当前用户消息中的依据")
	}
	item, err := p.store.AddEpisode(role, companion.EpisodeDraft{Title: args.Title, Content: args.Content, Status: "pending", EventAt: args.EventAt, FollowupAt: args.FollowupAt, AllowFollowup: args.AllowFollowup, SourceSessionID: session, SourceText: args.Evidence})
	if err != nil {
		return pluginapi.Result{}, errors.New("事件候选保存失败，请在事件面板检查内容与时间")
	}
	out, err := json.Marshal(item)
	if err != nil {
		return pluginapi.Result{}, err
	}
	return pluginapi.Result{Output: string(out)}, nil
}
