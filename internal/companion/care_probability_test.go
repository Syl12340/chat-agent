package companion

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// One lifecycle covers the persisted decision, concurrent poll/restart
// deduplication, attempt budget and explicit/manual bypass.
func TestCareProbabilityLifecycle(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenLife(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	location := time.FixedZone("fixture", 8*3600)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, location)
	p, err := s.Settings("companion")
	if err != nil {
		t.Fatal(err)
	}
	p.Enabled = true
	p.AutoGenerate = true
	p.DailyTime = "09:00"
	p.QuietStart = "00:00"
	p.QuietEnd = "00:00"
	p.DailyLimit = 5
	p.MinIntervalMinutes = 15
	p.AutoProbability = intPointer(50)
	p, err = s.SetSettings("companion", p)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Tick("companion", now, location); err != nil {
		t.Fatal(err)
	}
	items, _ := s.Care("companion")
	id := items[0].ID
	draws := 0
	s.careRoll = func() (int, error) { draws++; return 99, nil }
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = s.ClaimCare("companion", id, true, now, location) }()
	}
	wg.Wait()
	items, _ = s.Care("companion")
	if draws != 1 || items[0].Status != "skipped" || items[0].AttemptedAt != nil || items[0].DecisionAt == nil || len(s.state.Attempts) != 0 {
		t.Fatalf("skip consumed budget or resampled: draws=%d item=%+v attempts=%v", draws, items[0], s.state.Attempts)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenLife(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.careRoll = func() (int, error) { draws++; return 0, nil }
	if _, err = s.ClaimCare("companion", id, true, now, location); !errors.Is(err, ErrCareBlocked) || draws != 1 {
		t.Fatal("restart redrew a settled opportunity")
	}
	if s.state.Version != 4 {
		t.Fatal("care state version was not upgraded")
	}
	// The next day's independent opportunity hits and reserves exactly once.
	nextDay := now.AddDate(0, 0, 1)
	if err = s.Tick("companion", nextDay, location); err != nil {
		t.Fatal(err)
	}
	items, _ = s.Care("companion")
	var dailyID string
	for _, item := range items {
		if item.Status == "due" {
			dailyID = item.ID
		}
	}
	hit, err := s.ClaimCare("companion", dailyID, true, nextDay, location)
	if err != nil || hit.Status != "generating" || hit.AttemptedAt == nil || draws != 2 {
		t.Fatalf("hit did not reserve once: %+v %v", hit, err)
	}
	if _, err = s.FinishCare("companion", dailyID, hit.Revision, "你好", ""); err != nil {
		t.Fatal(err)
	}
	p, _ = s.Settings("companion")
	p.AutoProbability = intPointer(0)
	p, err = s.SetSettings("companion", p)
	if err != nil {
		t.Fatal(err)
	}
	third := now.AddDate(0, 0, 2)
	if err = s.Tick("companion", third, location); err != nil {
		t.Fatal(err)
	}
	items, _ = s.Care("companion")
	for _, item := range items {
		if item.Status == "due" {
			dailyID = item.ID
		}
	}
	manual, err := s.ClaimCare("companion", dailyID, false, third, location)
	if err != nil || manual.Status != "generating" || draws != 2 {
		t.Fatalf("manual generation was sampled: %+v %v", manual, err)
	}
	if _, err = s.FinishCare("companion", dailyID, manual.Revision, "你好", ""); err != nil {
		t.Fatal(err)
	}
	due := third.Add(time.Hour)
	event, err := s.AddEpisode("companion", EpisodeDraft{Title: "明确跟进", Content: "用户明确要求跟进", Status: "open", AllowFollowup: true, FollowupAt: &due})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Tick("companion", due, location); err != nil {
		t.Fatal(err)
	}
	items, _ = s.Care("companion")
	var eventID string
	for _, item := range items {
		if item.EpisodeID == event.ID {
			eventID = item.ID
		}
	}
	followup, err := s.ClaimCare("companion", eventID, true, due, location)
	if err != nil || followup.Status != "generating" || draws != 2 {
		t.Fatalf("explicit followup was sampled: %+v %v", followup, err)
	}
	prepared, err := s.PrepareCareDelivery("companion", eventID, followup.Revision, "按约定来问问。", "care-session", 4)
	if err != nil || prepared.Status != "delivering" {
		t.Fatalf("care delivery was exposed before transcript commit: %+v %v", prepared, err)
	}
	visible, _ := s.Care("companion")
	for _, item := range visible {
		if item.ID == eventID && (item.Content != "" || item.SessionID != "" || item.MessageIndex != nil) {
			t.Fatalf("prepared care leaked through the inbox: %+v", item)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenLife(dir)
	if err != nil {
		t.Fatal(err)
	}
	items, _ = s.Care("companion")
	recovered := false
	for _, item := range items {
		if item.ID == eventID {
			recovered = true
			if item.Status != "interrupted" || item.SessionID != "" || item.MessageIndex != nil || item.Content != "" {
				t.Fatalf("prepared delivery survived restart as visible: %+v", item)
			}
		}
	}
	if !recovered {
		t.Fatal("prepared care disappeared during recovery")
	}
}
