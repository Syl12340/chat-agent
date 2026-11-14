package api

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"chat-agent/internal/companion"
	"chat-agent/internal/probe"
)

func (s *Server) activityChatTools(role string, inputs []string, sessionID string) []probe.Tool {
	var mu sync.Mutex
	saved := map[string]probe.ToolResult{}
	encode := func(item companion.Activity, progressSaved bool) probe.ToolResult {
		result := map[string]any{"revision": item.Revision, "status": item.Status, "progress_saved": progressSaved}
		if item.Status == "active" {
			result["goal"], result["progress"] = item.Goal, item.Progress
		}
		out, _ := json.Marshal(result)
		return probe.ToolResult{Output: string(out)}
	}
	return []probe.Tool{{Name: "shared_activity_update", Usage: "update only the activity bound to this conversation, quoting the current turn as evidence.", Order: 610, Description: "Save progress and/or change status only for the activity already bound to this conversation, on the latest user's explicit request. Evidence is a short exact quote from a current-turn user message. Infer the intended action from conversation context, without command syntax. Use the latest activity_state or tool-result revision. Call shared_activity_open first if no activity is bound. Save only progress actually stated or explicitly referenced by the user, not inferred feelings or personality. Never save credentials. An empty progress removes the saved note; use only when that lasting effect is intended. Status active resumes, paused pauses, completed ends the activity. Ordinary fatigue, a good night or a topic change does not authorize persistent updates. A successful result overrides the initial snapshot; acknowledge only actual success. This is not an event, reminder or general memory tool.", Parameters: json.RawMessage(`{"type":"object","properties":{"revision":{"type":"integer","minimum":1},"evidence":{"type":"string","maxLength":8192},"progress":{"type":"string","maxLength":4096},"status":{"type":"string","enum":["active","paused","completed"]}},"required":["revision","evidence"],"additionalProperties":false}`), Execute: func(ctx context.Context, data json.RawMessage) (probe.ToolResult, error) {
		mu.Lock()
		defer mu.Unlock()
		if err := ctx.Err(); err != nil {
			return probe.ToolResult{}, err
		}
		var args struct {
			Revision int     `json:"revision"`
			Evidence string  `json:"evidence"`
			Progress *string `json:"progress"`
			Status   *string `json:"status"`
		}
		if err := toolInput(data, &args); err != nil {
			return probe.ToolResult{}, err
		}
		if !probe.TurnHasEvidence(inputs, args.Evidence) || args.Revision < 1 || (args.Progress == nil && args.Status == nil) {
			return probe.ToolResult{}, errors.New("需要当前用户消息的依据、有效版本和待修改字段")
		}
		canonical, _ := json.Marshal(args)
		if result, ok := saved[string(canonical)]; ok {
			return result, nil
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closing || ctx.Err() != nil || s.runs[sessionID] == nil {
			return probe.ToolResult{}, errors.New("对话已停止，活动没有修改")
		}
		for id := range s.runs {
			if id != sessionID && s.sessions[id] != nil && s.sessions[id].RoleID() == role {
				return probe.ToolResult{}, errors.New("这个角色的另一段对话正在运行，请停止后再修改活动")
			}
		}
		current, err := s.activities.Session(role, sessionID)
		if err != nil || current == nil || current.Revision != args.Revision {
			return probe.ToolResult{}, errors.New("活动已更新或删除，请查看最新记录后再试")
		}
		cuts, err := s.roleContextCuts(role, sessionID)
		if err != nil {
			return probe.ToolResult{}, errors.New("活动上下文暂时不可用，没有保存修改")
		}
		item, err := s.activities.Patch(role, current.ID, args.Revision, companion.ActivityPatch{Progress: args.Progress, Status: args.Status}, cuts)
		if err != nil {
			return probe.ToolResult{}, errors.New("活动修改没有保存，请在共同活动面板查看后重试")
		}
		result := encode(item, args.Progress != nil)
		saved[string(canonical)] = result
		return result, nil
	}}}
}
