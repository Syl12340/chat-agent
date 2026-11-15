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
