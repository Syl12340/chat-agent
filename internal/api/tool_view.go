package api

import (
	"strings"
	"time"

	"pocket-agent/internal/probe"
	"pocket-agent/internal/turns"
)

func startToolView(event probe.Event) turns.ToolEvent {
	return turns.ToolEvent{ID: event.ToolCall.ID, Name: event.ToolCall.Function.Name, View: event.ToolView, Status: "running"}
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
