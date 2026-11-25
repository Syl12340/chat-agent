package api

import (
	"chat-agent/internal/companion"
	"testing"
	"time"
)

// The motivation block is what the model reads to decide whether this check-in
// is welcome. Counting a rated follow-up as a rating on greetings would tell it
// the wrong story about why it is speaking.
func TestCareMotivationCountsFeedbackOfItsOwnKind(t *testing.T) {
	now := time.Date(2026, 11, 12, 23, 0, 0, 0, time.UTC)
	history := []companion.CareItem{
		{EpisodeID: "event-a", Feedback: "less", CreatedAt: now.Add(-time.Hour)},
		{Feedback: "helpful", CreatedAt: now.Add(-2 * time.Hour)},
	}

	greeting := careMotivation(history, companion.CareItem{DueAt: now}, now)
	if greeting["recent_helpful_feedback"] != 1 || greeting["recent_less_feedback"] != 0 {
		t.Fatalf("a greeting must count only greeting feedback: %+v", greeting)
	}
	// The whole-role count stays visible so the model can still see the wider mood.
	if greeting["recent_less_feedback_any"] != 1 {
		t.Fatalf("the role-wide count must still be reported: %+v", greeting)
	}

	followup := careMotivation(history, companion.CareItem{EpisodeID: "event-a", DueAt: now}, now)
	if followup["recent_less_feedback"] != 1 || followup["recent_helpful_feedback"] != 0 {
		t.Fatalf("a follow-up must count only follow-up feedback: %+v", followup)
	}
	if followup["source_kind"] != "approved_event_followup" {
		t.Fatalf("source_kind = %v", followup["source_kind"])
	}
}
