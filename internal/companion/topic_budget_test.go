package companion

import (
	"errors"
	"testing"
	"time"
)

func TestSharedTopicCareBudget(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenLife(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	loc := time.UTC
	p, _ := s.Settings("companion")
	p.Enabled = true
	p.AutoGenerate = true
	p.DailyLimit = 1
	p.QuietStart = "00:00"
	p.QuietEnd = "00:00"
	p.DailyTime = "00:00"
	if _, err = s.SetSettings("companion", p); err != nil {
		t.Fatal(err)
	}
	if err = s.ReserveTopicAttempt("companion", "budget-topic", now, loc); err != nil {
		t.Fatal(err)
	}
	if err = s.Tick("companion", now, loc); err != nil {
		t.Fatal(err)
	}
	care, _ := s.Care("companion")
	if _, err = s.ClaimCare("companion", care[0].ID, false, now.Add(4*time.Hour), loc); !errors.Is(err, ErrCareBlocked) {
		t.Fatal("topic bypassed shared daily care budget")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenLife(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.state.Version != 5 || s.TopicStillAllowed("companion", "budget-topic", now, loc) != nil {
		t.Fatal("topic budget receipt did not survive restart")
	}
	if err = s.ReserveTopicAttempt("companion", "budget-topic", now, loc); !errors.Is(err, ErrCareBlocked) {
		t.Fatal("receipt replay granted another attempt")
	}
}
