package api

import (
	"testing"
	"time"

	"chat-agent/internal/companion"
	"chat-agent/internal/probe"
)

func careItem(status, session string, index int, at time.Time) companion.CareItem {
	idx := index
	return companion.CareItem{
		ID: "care-" + status, Status: status, Content: "该睡了", SessionID: session,
		MessageIndex: &idx, CreatedAt: at, AttemptedAt: &at,
	}
}

func TestNewestCheckInIgnoresWhatCannotBeAnswered(t *testing.T) {
	now := time.Date(2026, 11, 12, 23, 59, 0, 0, time.UTC)
	recent := now.Add(-30 * time.Minute)
	for _, row := range []struct {
		name  string
		item  companion.CareItem
		found bool
	}{
		{"delivered", careItem("seen", "session-a", 4, recent), true},
		{"silenced", careItem("silent", "session-a", 4, recent), false},
		{"failed", careItem("failed", "session-a", 4, recent), false},
		{"no content", companion.CareItem{Status: "seen", SessionID: "session-a", MessageIndex: new(int), CreatedAt: recent}, false},
		{"never placed", companion.CareItem{Status: "seen", Content: "该睡了", CreatedAt: recent}, false},
		{"stale", careItem("seen", "session-a", 4, now.Add(-13*time.Hour)), false},
	} {
		if _, ok := newestCheckIn([]companion.CareItem{row.item}, now); ok != row.found {
			t.Fatalf("%s: found = %v", row.name, ok)
		}
	}
}

func TestNewestCheckInPicksTheLatestDelivered(t *testing.T) {
	now := time.Date(2026, 11, 12, 23, 59, 0, 0, time.UTC)
	history := []companion.CareItem{
		careItem("seen", "session-a", 4, now.Add(-40*time.Minute)),
		careItem("seen", "session-a", 9, now.Add(-10*time.Minute)),
		careItem("silent", "session-a", 12, now.Add(-time.Minute)),
	}
	last, ok := newestCheckIn(history, now)
	if !ok || *last.MessageIndex != 9 {
		t.Fatalf("picked %+v", last)
	}
}

func TestCheckInsForEpisodeCountsOnlyWhatWasDelivered(t *testing.T) {
	item := func(status, episode, content string) companion.CareItem {
		return companion.CareItem{Status: status, EpisodeID: episode, Content: content}
	}
	history := []companion.CareItem{
		item("seen", "event-a", "该睡了"),
		item("ready", "event-a", "明天再写"),
		item("silent", "event-a", ""),
		item("failed", "event-a", "出错了"),
		item("seen", "event-b", "别的"),
	}
	if got := checkInsForEpisode(history, "event-a"); got != 2 {
		t.Fatalf("event-a = %d, want 2", got)
	}
	if got := checkInsForEpisode(history, "event-b"); got != 1 {
		t.Fatalf("event-b = %d, want 1", got)
	}
	// An event-less opportunity must not inherit another event's count.
	if got := checkInsForEpisode(history, ""); got != 0 {
		t.Fatalf("empty episode = %d, want 0", got)
	}
}

func TestCareMotivationReportsStaleness(t *testing.T) {
	now := time.Date(2026, 11, 12, 23, 0, 0, 0, time.UTC)
	claimed := companion.CareItem{EpisodeID: "event-a", DueAt: now.Add(-3 * time.Hour)}
	history := []companion.CareItem{{Status: "seen", EpisodeID: "event-a", Content: "该睡了", Feedback: "less", CreatedAt: now.Add(-time.Hour)}}
	motivation := careMotivation(history, claimed, now)
	if motivation["hours_since_due"] != 3 {
		t.Fatalf("hours_since_due = %v", motivation["hours_since_due"])
	}
	if motivation["check_ins_for_this_episode"] != 1 {
		t.Fatalf("check_ins = %v", motivation["check_ins_for_this_episode"])
	}
	if motivation["has_event"] != true {
		t.Fatalf("has_event = %v", motivation["has_event"])
	}
	if motivation["source_kind"] != "approved_event_followup" || motivation["recent_less_feedback"] != 1 || motivation["recent_helpful_feedback"] != 0 {
		t.Fatalf("motivation feedback/source = %+v", motivation)
	}
	// A daily greeting carries no event, and must not claim one.
	daily := careMotivation(nil, companion.CareItem{DueAt: now}, now)
	if _, ok := daily["has_event"]; ok {
		t.Fatal("event-less opportunity reported has_event")
	}
	if daily["source_kind"] != "optional_daily_greeting" {
		t.Fatalf("daily source = %v", daily["source_kind"])
	}
}

func TestAnsweredAfter(t *testing.T) {
	messages := []probe.Message{
		{Role: "user", Content: "在写文档"},
		{Role: "assistant", Content: "该睡了"},
		{Role: "assistant", Content: "明天再写"},
	}
	if answeredAfter(messages, 1) {
		t.Fatal("two assistant messages counted as an answer")
	}
	messages = append(messages, probe.Message{Role: "user", Content: "好"})
	if !answeredAfter(messages, 1) {
		t.Fatal("a later user message did not count as an answer")
	}
	// A message before the check-in is not a reply to it.
	if answeredAfter(messages, 3) {
		t.Fatal("an earlier reply was counted for a later check-in")
	}
}
