package api

import (
	"strings"
	"time"

	"chat-agent/internal/probe"
	"chat-agent/internal/turns"
)

func startToolView(event probe.Event) turns.ToolEvent {
	return turns.ToolEvent{
		ID:   event.ToolCall.ID,
		Name: event.ToolCall.Function.Name,
		// Keep a bounded copy of the arguments. Without them the card can say
		// which tool ran but not what it was asked to do, which is the half a
		// reader needs to judge the result.
		Arguments: truncate(event.ToolCall.Function.Arguments, 512),
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
	if result.Error != "" {
		tool.Status = "unfinished"
	}
	if result.Code == "DECISION_NEEDS_APPROVAL" && result.Decision != nil && result.Decision.Approval != nil {
		tool.Status = "approval_required"
		tool.Approval = &probe.ApprovalRequest{Tool: result.Decision.Approval.Tool, Digest: result.Decision.Approval.Digest}
	}
}
