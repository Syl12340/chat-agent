package companion

import (
	"testing"
	"time"
)

// The acceptance case: "fewer of these" should thin out daily greetings while a
// follow-up the person agreed to still arrives on time.
func TestCareKindSeparatesGreetingsFromFollowUps(t *testing.T) {
	greeting := CareItem{RoleID: "companion", Title: "今天想和你聊一会儿"}
	followup := CareItem{RoleID: "companion", EpisodeID: "ep1", Title: "面试结果"}
	if got := CareKind(greeting); got != "greeting" {
		t.Fatalf("want greeting, got %q", got)
	}
	if got := CareKind(followup); got != "followup" {
		t.Fatalf("want followup, got %q", got)
	}
}

func TestCareKindMutedIsScopedToTheRatedKind(t *testing.T) {
	now := time.Now()
	items := []CareItem{
		{RoleID: "companion", Feedback: "less", CreatedAt: now.Add(-time.Hour)},
		{RoleID: "companion", Feedback: "less", CreatedAt: now.Add(-2 * time.Hour)},
	}
	if !CareKindMuted(items, "companion", "greeting", now) {
		t.Fatal("a majority of less ratings should mute greetings")
	}
	// The follow-up kind was never rated, so it keeps its normal pace.
	if CareKindMuted(items, "companion", "followup", now) {
		t.Fatal("unrated follow-ups must not be muted by greeting feedback")
	}
	// Another role's ratings are not this role's business.
	if CareKindMuted(items, "other", "greeting", now) {
		t.Fatal("feedback must stay role-scoped")
	}
}

func TestCareKindMutedNeedsAMajorityAndExpires(t *testing.T) {
	now := time.Now()
	// Half asking for less is enough, matching careInterval: one explicit "less"
	// must not be cancelled by an earlier "more".
	tie := []CareItem{
		{RoleID: "companion", Feedback: "less", CreatedAt: now.Add(-time.Hour)},
		{RoleID: "companion", Feedback: "more", CreatedAt: now.Add(-time.Hour)},
	}
	if !CareKindMuted(tie, "companion", "greeting", now) {
		t.Fatal("half the ratings asking for less should mute")
	}
	// A minority asking for less does not.
	mostlyMore := []CareItem{
		{RoleID: "companion", Feedback: "more", CreatedAt: now.Add(-time.Hour)},
		{RoleID: "companion", Feedback: "more", CreatedAt: now.Add(-2 * time.Hour)},
		{RoleID: "companion", Feedback: "less", CreatedAt: now.Add(-3 * time.Hour)},
	}
	if CareKindMuted(mostlyMore, "companion", "greeting", now) {
		t.Fatal("a minority asking for less must not mute")
	}
	stale := []CareItem{{RoleID: "companion", Feedback: "less", CreatedAt: now.Add(-40 * 24 * time.Hour)}}
	if CareKindMuted(stale, "companion", "greeting", now) {
		t.Fatal("feedback older than 30 days must expire")
	}
	// A follow-up carrying "less" must not silence greetings: the rating belongs
	// to the kind it was given on.
	mixed := []CareItem{{RoleID: "companion", EpisodeID: "ep1", Feedback: "less", CreatedAt: now.Add(-time.Hour)}}
	if CareKindMuted(mixed, "companion", "greeting", now) {
		t.Fatal("follow-up feedback must not mute greetings")
	}
	if !CareKindMuted(mixed, "companion", "followup", now) {
		t.Fatal("follow-up feedback must mute follow-ups")
	}
}
