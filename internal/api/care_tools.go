package api

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"chat-agent/internal/companion"
	"chat-agent/internal/probe"
)

func (s *Server) careTools(role string, inputs []string, sessionID string) []probe.Tool {
	var mu sync.Mutex
	saved := map[string]probe.ToolResult{}
	return []probe.Tool{{Name: "care_preferences", Usage: "read the settings first; enabling proactive care needs the user's explicit ongoing opt-in, and a settings change sends nothing by itself.", Order: 800, Description: "Read or adjust this role's optional proactive care settings from conversation intent. action=read has no side effects. action=update changes only fields the user actually wants: enabled/auto_generate require genuine opt-in, never infer it from an event, fatigue or ordinary small talk. action=snooze pauses future generation until snooze_until without deleting delivered history; action=resume clears snooze only and does not enable a disabled plan. For writes, include current revision from read/result and a short exact current-turn user quote as evidence. Daily limit is 1..5, interval is 15..1440 minutes. auto_probability is 0..100 percent and affects only optional automatic daily greetings, never explicit event followups or manual generation. Quiet hours and snooze use the configured user timezone; snooze_until must include an RFC3339 offset. This tool never generates a paid check-in or schedules external messages. It does not grant closed-app push delivery.", Parameters: json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["read","update","snooze","resume"]},"revision":{"type":"integer","minimum":0},"evidence":{"type":"string","maxLength":8192},"enabled":{"type":"boolean"},"auto_generate":{"type":"boolean"},"auto_probability":{"type":"integer","minimum":0,"maximum":100},"daily_limit":{"type":"integer","minimum":1,"maximum":5},"min_interval_minutes":{"type":"integer","minimum":15,"maximum":1440},"quiet_start":{"type":"string"},"quiet_end":{"type":"string"},"daily_time":{"type":"string"},"snooze_until":{"type":["string","null"]}},"required":["action"],"additionalProperties":false}`), Execute: func(ctx context.Context, data json.RawMessage) (probe.ToolResult, error) {
		mu.Lock()
		defer mu.Unlock()
		if err := ctx.Err(); err != nil {
			return probe.ToolResult{}, err
		}
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
		if err := toolInput(data, &args); err != nil {
			return probe.ToolResult{}, err
		}
		p, err := s.life.Settings(role)
		if err != nil {
			return probe.ToolResult{}, errors.New("关怀设置暂不可用")
		}
		changedFields := args.Enabled != nil || args.AutoGenerate != nil || args.AutoProbability != nil || args.DailyLimit != nil || args.MinIntervalMinutes != nil || args.QuietStart != nil || args.QuietEnd != nil || args.DailyTime != nil || args.SnoozeUntil != nil
		if args.Action == "read" {
			if changedFields {
				return probe.ToolResult{}, errors.New("读取不能同时修改设置")
			}
			return encoded(p)
		}
		if !turnHasEvidence(inputs, args.Evidence) {
			return probe.ToolResult{}, errors.New("关怀设置修改缺少当前用户消息中的依据")
		}
		if args.Action != "update" && args.Action != "snooze" && args.Action != "resume" {
			return probe.ToolResult{}, errors.New("未知关怀设置操作")
		}
		keyArgs := args
		keyArgs.Evidence = ""
		canonical, _ := json.Marshal(keyArgs)
		if result, ok := saved[string(canonical)]; ok {
			return result, nil
		}
		if args.Revision != p.Revision {
			return probe.ToolResult{}, errors.New("关怀设置已变化，请读取最新版本后再决定")
		}
		p.Revision = args.Revision
		if args.Action == "update" {
			if !changedFields {
				return probe.ToolResult{}, errors.New("请提供希望修改的关怀选项")
			}
			if args.Enabled != nil {
				p.Enabled = *args.Enabled
			}
			if args.AutoGenerate != nil {
				p.AutoGenerate = *args.AutoGenerate
			}
			if args.AutoProbability != nil {
				p.AutoProbability = args.AutoProbability
			}
			if args.DailyLimit != nil {
				p.DailyLimit = *args.DailyLimit
			}
			if args.MinIntervalMinutes != nil {
				if *args.MinIntervalMinutes < 15 || *args.MinIntervalMinutes > 1440 {
					return probe.ToolResult{}, companion.ErrInvalid
				}
				p.MinIntervalMinutes = *args.MinIntervalMinutes
			}
			if args.QuietStart != nil {
				p.QuietStart = *args.QuietStart
			}
			if args.QuietEnd != nil {
				p.QuietEnd = *args.QuietEnd
			}
			if args.DailyTime != nil {
				p.DailyTime = *args.DailyTime
			}
			if args.SnoozeUntil != nil {
				if err := json.Unmarshal(args.SnoozeUntil, &p.SnoozeUntil); err != nil {
					return probe.ToolResult{}, errors.New("暂停时间需要带时区的日期或null")
				}
			}
		} else {
			if args.Enabled != nil || args.AutoGenerate != nil || args.AutoProbability != nil || args.DailyLimit != nil || args.MinIntervalMinutes != nil || args.QuietStart != nil || args.QuietEnd != nil || args.DailyTime != nil {
				return probe.ToolResult{}, errors.New("暂停或恢复不能同时修改其他关怀设置")
			}
			if args.Action == "snooze" {
				if args.SnoozeUntil == nil || json.Unmarshal(args.SnoozeUntil, &p.SnoozeUntil) != nil || p.SnoozeUntil == nil || !p.SnoozeUntil.After(time.Now()) {
					return probe.ToolResult{}, errors.New("请指定未来的暂停截止时间")
				}
			} else {
				p.SnoozeUntil = nil
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closing || ctx.Err() != nil || s.runs[sessionID] == nil {
			return probe.ToolResult{}, errors.New("对话已停止，没有修改关怀设置")
		}
		for id := range s.runs {
			if id != sessionID && s.sessions[id] != nil && s.sessions[id].RoleID() == role {
				return probe.ToolResult{}, errors.New("同角色其他对话正在运行，请稍后再调整关怀")
			}
		}
		p, err = s.life.SetSettings(role, p)
		if err != nil {
			return probe.ToolResult{}, errors.New("关怀设置未保存，请读取最新版本后核对")
		}
		result, err := encoded(p)
		if err == nil {
			saved[string(canonical)] = result
		}
		return result, err
	}}}
}
