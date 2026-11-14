// Package care exposes this role's proactive-care settings as a tool.
//
// It is the first host tool that needed two capabilities the plugin contract did
// not have: the current turn's user text, so a "the user asked for this" claim
// can be checked against a real quote, and a way to commit role-scoped settings
// without racing another conversation of the same role. probe.ToolInputs and
// pluginapi.WriteGate exist because of it; earlier candidates such as
// care_preferences could not leave the API package without them.
//
// The tool deliberately keeps no memory across calls. A retried write fails its
// revision check and the model re-reads, which is honest and cheap; a cached
// success would be a lie the moment another turn changed the settings.
package care

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"chat-agent/internal/companion"
	"chat-agent/internal/pluginapi"
	"chat-agent/internal/probe"
)

const PluginID = "builtin.care"

// Store is the narrow slice of the life store this tool uses. Both methods are
// role-scoped and revisioned, so a write can be rejected as stale.
type Store interface {
	Settings(role string) (companion.CareSettings, error)
	SetSettings(role string, p companion.CareSettings) (companion.CareSettings, error)
}

// New returns nil when care has no store or no write gate, matching the other
// builtins: a host that cannot serialize the write must not offer the tool.
func New(store Store, gate pluginapi.WriteGate) pluginapi.Plugin {
	if store == nil || gate == nil {
		return nil
	}
	return &Plugin{
		store: store,
		gate:  gate,
		manifest: pluginapi.Manifest{
			APIVersion: pluginapi.APIVersion,
			Kind:       pluginapi.Kind,
			Metadata: pluginapi.Metadata{
				ID:          PluginID,
				Name:        "关怀设置",
				Version:     "0.1.0",
				Description: "按角色读取和调整主动关怀的节奏与开关",
			},
			Spec: pluginapi.Spec{
				Runtime:      pluginapi.RuntimeBuiltin,
				Capabilities: []string{"care.settings"},
				Tools:        []string{"care_preferences"},
			},
		},
	}
}

// Plugin answers "when may this role reach out on its own".
type Plugin struct {
	store    Store
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
		Name:  "care_preferences",
		Order: 800,
		Usage: "read the settings first; enabling proactive care needs the user's explicit ongoing opt-in, and a settings change sends nothing by itself.",
		Description: "Read or adjust this role's optional proactive care settings from conversation intent. " +
			"action=read has no side effects. action=update changes only fields the user actually wants: " +
			"enabled/auto_generate require genuine opt-in, never infer it from an event, fatigue or ordinary small talk. " +
			"action=snooze pauses future generation until snooze_until without deleting delivered history; " +
			"action=resume clears snooze only and does not enable a disabled plan. " +
			"For writes, include current revision from read/result and a short exact current-turn user quote as evidence. " +
			"Daily limit is 1..5, interval is 15..1440 minutes. auto_probability is 0..100 percent and affects only " +
			"optional automatic daily greetings, never explicit event followups or manual generation. " +
			"Quiet hours and snooze use the configured user timezone; snooze_until must include an RFC3339 offset. " +
			"This tool never generates a paid check-in or schedules external messages. " +
			"It does not grant closed-app push delivery.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["read","update","snooze","resume"]},"revision":{"type":"integer","minimum":0},"evidence":{"type":"string","maxLength":8192},"enabled":{"type":"boolean"},"auto_generate":{"type":"boolean"},"auto_probability":{"type":"integer","minimum":0,"maximum":100},"daily_limit":{"type":"integer","minimum":1,"maximum":5},"min_interval_minutes":{"type":"integer","minimum":15,"maximum":1440},"quiet_start":{"type":"string"},"quiet_end":{"type":"string"},"daily_time":{"type":"string"},"snooze_until":{"type":["string","null"]}},"required":["action"],"additionalProperties":false}`),
		Handler:    p.handle,
	}
}

func (p *Plugin) handle(ctx context.Context, data json.RawMessage) (pluginapi.Result, error) {
	var args struct {
		Action             string          `json:"action"`
		Revision           int             `json:"revision"`
		Evidence           string          `json:"evidence"`
		Enabled            *bool           `json:"enabled"`
		AutoGenerate       *bool           `json:"auto_generate"`
		AutoProbability    *int            `json:"auto_probability"`
		DailyLimit         *int            `json:"daily_limit"`
		MinIntervalMinutes *int            `json:"min_interval_minutes"`
		QuietStart         *string         `json:"quiet_start"`
		QuietEnd           *string         `json:"quiet_end"`
		DailyTime          *string         `json:"daily_time"`
		SnoozeUntil        json.RawMessage `json:"snooze_until"`
	}
	if err := json.Unmarshal(data, &args); err != nil {
		return pluginapi.Result{}, err
	}
	role := probe.ToolRole(ctx)
	session := probe.ToolSession(ctx)
	if role == "" || session == "" {
		return pluginapi.Result{}, errors.New("工具上下文缺少角色或会话")
	}
	p0, err := p.store.Settings(role)
	if err != nil {
		return pluginapi.Result{}, errors.New("关怀设置暂不可用")
	}
	changedFields := args.Enabled != nil || args.AutoGenerate != nil || args.AutoProbability != nil || args.DailyLimit != nil || args.MinIntervalMinutes != nil || args.QuietStart != nil || args.QuietEnd != nil || args.DailyTime != nil || args.SnoozeUntil != nil
	if args.Action == "read" {
		if changedFields {
			return pluginapi.Result{}, errors.New("读取不能同时修改设置")
		}
		return encode(p0)
	}
	if !probe.TurnHasEvidence(probe.ToolInputs(ctx), args.Evidence) {
		return pluginapi.Result{}, errors.New("关怀设置修改缺少当前用户消息中的依据")
	}
	if args.Action != "update" && args.Action != "snooze" && args.Action != "resume" {
		return pluginapi.Result{}, errors.New("未知关怀设置操作")
	}
	if args.Revision != p0.Revision {
		return pluginapi.Result{}, errors.New("关怀设置已变化，请读取最新版本后再决定")
	}
	p0.Revision = args.Revision
	if args.Action == "update" {
		if !changedFields {
			return pluginapi.Result{}, errors.New("请提供希望修改的关怀选项")
		}
		if args.Enabled != nil {
			p0.Enabled = *args.Enabled
		}
		if args.AutoGenerate != nil {
			p0.AutoGenerate = *args.AutoGenerate
		}
		if args.AutoProbability != nil {
			p0.AutoProbability = args.AutoProbability
		}
		if args.DailyLimit != nil {
			p0.DailyLimit = *args.DailyLimit
		}
		if args.MinIntervalMinutes != nil {
			if *args.MinIntervalMinutes < 15 || *args.MinIntervalMinutes > 1440 {
				return pluginapi.Result{}, companion.ErrInvalid
			}
			p0.MinIntervalMinutes = *args.MinIntervalMinutes
		}
		if args.QuietStart != nil {
			p0.QuietStart = *args.QuietStart
		}
		if args.QuietEnd != nil {
			p0.QuietEnd = *args.QuietEnd
		}
		if args.DailyTime != nil {
			p0.DailyTime = *args.DailyTime
		}
		if args.SnoozeUntil != nil {
			if err := json.Unmarshal(args.SnoozeUntil, &p0.SnoozeUntil); err != nil {
				return pluginapi.Result{}, errors.New("暂停时间需要带时区的日期或null")
			}
		}
	} else {
		if args.Enabled != nil || args.AutoGenerate != nil || args.AutoProbability != nil || args.DailyLimit != nil || args.MinIntervalMinutes != nil || args.QuietStart != nil || args.QuietEnd != nil || args.DailyTime != nil {
			return pluginapi.Result{}, errors.New("暂停或恢复不能同时修改其他关怀设置")
		}
		if args.Action == "snooze" {
			if args.SnoozeUntil == nil || json.Unmarshal(args.SnoozeUntil, &p0.SnoozeUntil) != nil || p0.SnoozeUntil == nil || !p0.SnoozeUntil.After(time.Now()) {
				return pluginapi.Result{}, errors.New("请指定未来的暂停截止时间")
			}
		} else {
			p0.SnoozeUntil = nil
		}
	}
	if err := ctx.Err(); err != nil {
		return pluginapi.Result{}, err
	}
	saved := p0
	err = p.gate.WithRoleWrite(role, session, func(pluginapi.WriteScope) error {
		next, err := p.store.SetSettings(role, saved)
		if err != nil {
			return err
		}
		saved = next
		return nil
	})
	switch {
	case errors.Is(err, pluginapi.ErrWriteSessionEnded):
		return pluginapi.Result{}, errors.New("对话已停止，没有修改关怀设置")
	case errors.Is(err, pluginapi.ErrWriteRoleBusy):
		return pluginapi.Result{}, errors.New("同角色其他对话正在运行，请稍后再调整关怀")
	case err != nil:
		return pluginapi.Result{}, errors.New("关怀设置未保存，请读取最新版本后核对")
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
