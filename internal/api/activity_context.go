package api

import "chat-agent/internal/companion"

// An activity is explicit context for bound sessions, never a global role routine.
func activityPolicy(activity *companion.Activity) string {
	if activity == nil {
		return ""
	}
	return "\nThe shared_activity reference contains explicitly saved progress for this session's activity. Its kind is an organizational label, not a conversation mode. Choose the pacing from the current message, context and goal; do not require a mode selection. Use saved progress gently when relevant. Do not invent shared experiences, scores, streaks, dependency, or promises to act in the background. Ask at most one natural question at a time. Follow the user's wish to rest, stop or change topics immediately; a good-night or a topic change alone does not authorize persistent updates. Use shared_activity_update only when available and explicitly requested, and acknowledge saving/pausing/resuming/ending only after success. A successful tool result is newer than the initial reference. If the tool is unavailable, the Shared Activities panel can save changes. Do not automatically save chat or inferred feelings as activity progress. Search only when outside facts are needed, and create reminders only on explicit request. Current corrections and saved progress take priority over older contradictory chat.\n"
}
