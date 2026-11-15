package companion

import (
	"testing"
	"time"
)

func TestCareIntervalStretchesAfterLess(t *testing.T) {
	now := time.Date(2026, 11, 16, 0, 30, 0, 0, time.UTC)
	p := CareSettings{RoleID: "companion", MinIntervalMinutes: 60}
	item := func(feedback string, age time.Duration) CareItem {
		return CareItem{RoleID: "companion", Feedback: feedback, CreatedAt: now.Add(-age)}
	}

	if got := careInterval(p, nil, "companion", now); got != time.Hour {
		t.Fatalf("no feedback changed the floor: %v", got)
	}
	if got := careInterval(p, []CareItem{item("helpful", time.Hour)}, "companion", now); got != time.Hour {
		t.Fatalf("helpful changed the floor: %v", got)
	}
	// One "less" among mostly helpful ratings is a caution, not a verdict.
	mostlyFine := []CareItem{item("less", time.Hour), item("helpful", 2*time.Hour), item("helpful", 3*time.Hour)}
	if got := careInterval(p, mostlyFine, "companion", now); got != 2*time.Hour {
		t.Fatalf("single less = %v, want 2h", got)
	}
	// Half or more said "less": speak four times less often.
	both := []CareItem{item("less", time.Hour), item("less", 2*time.Hour)}
	if got := careInterval(p, both, "companion", now); got != 4*time.Hour {
		t.Fatalf("mostly less = %v, want 4h", got)
	}
	// Another role's feedback is not this role's business.
	elsewhere := []CareItem{{RoleID: "other", Feedback: "less", CreatedAt: now.Add(-time.Hour)}}
	if got := careInterval(p, elsewhere, "companion", now); got != time.Hour {
		t.Fatalf("other role leaked: %v", got)
	}
	// An old verdict must not silence the feature forever.
	stale := []CareItem{item("less", 60*24*time.Hour), item("less", 60*24*time.Hour)}
	if got := careInterval(p, stale, "companion", now); got != time.Hour {
		t.Fatalf("stale feedback still applied: %v", got)
	}
}

func TestCareIntervalHasACeiling(t *testing.T) {
	now := time.Date(2026, 11, 16, 0, 30, 0, 0, time.UTC)
	p := CareSettings{RoleID: "companion", MinIntervalMinutes: 12 * 60}
	items := []CareItem{
		{RoleID: "companion", Feedback: "less", CreatedAt: now.Add(-time.Hour)},
		{RoleID: "companion", Feedback: "less", CreatedAt: now.Add(-2 * time.Hour)},
	}
	if got := careInterval(p, items, "companion", now); got != 24*time.Hour {
		t.Fatalf("interval = %v, want the 24h ceiling", got)
	}
}
