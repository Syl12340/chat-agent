package api

import (
	"encoding/json"
	"strings"
	"time"

	"chat-agent/internal/probe"
	"chat-agent/internal/turns"
)

func startToolView(event probe.Event) turns.ToolEvent {
	call := event.ToolCall
	arguments := truncate(call.Function.Arguments, 512)
	if call.Function.Name == "workspace_shell" {
		// Show every executable character in the approval card. Canonical JSON
		// avoids retaining arbitrary whitespace or repeated keys from the model.
		var shellArgs struct {
			Workspace string `json:"workspace"`
			Command   string `json:"command"`
			Cwd       string `json:"cwd,omitempty"`
			Mode      string `json:"mode"`
		}
		if json.Unmarshal([]byte(call.Function.Arguments), &shellArgs) == nil && len(shellArgs.Command) <= 4096 && len(shellArgs.Cwd) <= 1024 {
			if encoded, err := json.Marshal(shellArgs); err == nil {
				arguments = string(encoded)
			}
		}
	}
	return turns.ToolEvent{
		ID:   call.ID,
		Name: call.Function.Name,
		// Keep a bounded copy of the arguments. Without them the card can say
		// which tool ran but not what it was asked to do, which is the half a
		// reader needs to judge the result.
		Arguments: arguments,
		View:      event.ToolView,
		Status:    "running",
	}
}

// truncate shortens by bytes and marks the cut, so a long argument list cannot
// push the rest of the record out of the retention window.
func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return strings.ToValidUTF8(text[:limit], "") + "…"
}

func finishToolView(tool *turns.ToolEvent, result *probe.ToolResult, elapsed time.Duration) {
	ms := elapsed.Milliseconds()
	tool.ElapsedMS = &ms
	tool.Status = "completed"
	tool.Output = result.Output
	if len(tool.Output) > 4096 {
		tool.Output = strings.ToValidUTF8(tool.Output[:4096], "") + "…"
	}
	tool.Error = result.Error
	tool.Approval = nil
	if result.Error != "" {
		tool.Status = "unfinished"
	}
	if result.Code == "DECISION_NEEDS_APPROVAL" && result.Decision != nil && result.Decision.Approval != nil {
		tool.Status = "approval_required"
		approval := *result.Decision.Approval
		tool.Approval = &approval
	}
}
