package topics

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestDiscoveryLifecycle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "topics")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			_ = s.Close()
		}
	}()
	loc := time.UTC
	clock := time.Date(2026, 11, 4, 8, 0, 0, 0, loc)
	s.now = func() time.Time { return clock }
	s.random = func(n int) (int, error) { return 0, nil }
	configure := func(role, start, end string, probability int) Settings {
		t.Helper()
		p, e := s.Settings(role)
		if e != nil {
			t.Fatal(e)
		}
		p.Enabled = true
		p.Queries = []string{"public science news"}
		p.WindowStart = start
		p.WindowEnd = end
		p.ScanProbability = probability
		p.VisitsPerDay = 1
		p, e = s.UpdateSettings(role, p)
		if e != nil {
			t.Fatal(e)
		}
		return p
	}
	ensure := func(role string) {
		t.Helper()
		if e := s.EnsureDay(role, clock, loc); e != nil {
			t.Fatal(e)
		}
	}
	rows := func(role string) []Record {
		t.Helper()
		r, e := s.Records(role)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	claim := func(role string, hour int) Record {
		t.Helper()
		clock = time.Date(2026, 11, 4, hour, 0, 0, 0, loc)
		r, e := s.ClaimDue(role, clock, loc)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	p, _ := s.Settings("skip")
	if p.Enabled || p.ScanProbability != 70 || p.VisitsPerDay != 2 || p.DailyLimit != 3 {
		t.Fatalf("unsafe defaults: %+v", p)
	}
	p = configure("skip", "09:00", "10:00", 0)
	ensure("skip")
	r := rows("skip")[0]
	if r.Status != "skipped" || r.SearchCount != 0 {
		t.Fatalf("skip performed work: %+v", r)
	}
	if err = s.DeleteRecord("skip", r.ID, r.Revision); err != nil {
		t.Fatal(err)
	}
	p.Enabled = false
	p, err = s.UpdateSettings("skip", p)
	if err != nil {
		t.Fatal(err)
	}
	p.Enabled = true
	p.ScanProbability = 100
	_, err = s.UpdateSettings("skip", p)
	if err != nil {
		t.Fatal(err)
	}
	ensure("skip")
	if len(rows("skip")) != 0 {
		t.Fatal("deletion/toggling redrew the daily opportunity")
	}
	for _, c := range []struct{ role, start, end string }{{"a", "09:00", "10:00"}, {"b", "12:00", "13:00"}, {"c", "15:00", "16:00"}, {"d", "18:00", "19:00"}, {"late", "09:00", "10:00"}} {
		configure(c.role, c.start, c.end, 100)
		ensure(c.role)
	}
	before := rows("a")[0].DueAt
	s.random = func(n int) (int, error) { return n - 1, nil }
	ensure("a")
	if !rows("a")[0].DueAt.Equal(before) {
		t.Fatal("existing schedule was redrawn")
	}
	configure("missed", "07:00", "08:00", 100)
	ensure("missed")
	if rows("missed")[0].Status != "expired" {
		t.Fatal("past initial slots must expire")
	}
	a := claim("a", 9)
	if _, err = s.ClaimDue("b", clock.Add(2*time.Hour), loc); !errors.Is(err, ErrBlocked) {
		t.Fatalf("spacing not enforced: %v", err)
	}
	a, err = s.Finish("a", a.ID, a.Revision, Result{Status: "ready", Content: "A public finding", Sources: []Source{{Title: "Source", URL: "https://example.com/article"}}, SearchCount: 1, ReadCount: 1, ModelCalls: 1})
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(3 * time.Hour)
	if _, err = s.ClaimDue("b", clock, loc); !errors.Is(err, ErrBlocked) {
		t.Fatalf("unread topic did not block global discovery: %v", err)
	}
	a, err = s.Mark("a", a.ID, "seen", a.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteRecord("a", a.ID, a.Revision); err != nil {
		t.Fatal(err)
	}
	b := claim("b", 12)
	b, err = s.Finish("b", b.ID, b.Revision, Result{Status: "failed", Reason: "public page unavailable", SearchCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteRecord("b", b.ID, b.Revision); err != nil {
		t.Fatal(err)
	}
	c := claim("c", 15)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return clock }
	s.random = func(n int) (int, error) { return 0, nil }
	c = rows("c")[0]
	if c.Status != "interrupted" {
		t.Fatalf("running work replayable after restart: %+v", c)
	}
	if err = s.DeleteRecord("c", c.ID, c.Revision); err != nil {
		t.Fatal(err)
	}
	clock = time.Date(2026, 11, 4, 18, 0, 0, 0, loc)
	if _, err = s.ClaimDue("d", clock, loc); !errors.Is(err, ErrBlocked) {
		t.Fatalf("failed/deleted/restarted claims reset global cap: %v", err)
	}
	ensure("a")
	if len(rows("a")) != 0 {
		t.Fatal("deleted charged opportunity was recreated")
	}
	if _, err = s.ClaimDue("late", clock, loc); !errors.Is(err, ErrBlocked) {
		t.Fatalf("out-of-window claim: %v", err)
	}
	if rows("late")[0].Status != "expired" {
		t.Fatal("missed slot remained runnable")
	}
	clock = time.Date(2026, 11, 5, 8, 0, 0, 0, loc)
	ensure("a")
	clock = clock.Add(time.Hour)
	a, err = s.ClaimDue("a", clock, loc)
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.Settings("a")
	if err != nil {
		t.Fatal(err)
	}
	p.Enabled = false
	if _, err = s.UpdateSettings("a", p); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Finish("a", a.ID, a.Revision, Result{Status: "ready", Content: "must not publish"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("disabled discovery published: %v", err)
	}
	for _, r = range rows("a") {
		if r.Status == "ready" || r.Status == "running" || r.Status == "scheduled" {
			t.Fatalf("disabled work survived: %+v", r)
		}
	}
}

func TestPreparedDeliveryRecovery(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "topics")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 11, 8, 8, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	s.random = func(int) (int, error) { return 0, nil }
	p, _ := s.Settings("companion")
	p.Enabled = true
	p.Queries = []string{"public science news"}
	p.WindowStart = "09:00"
	p.WindowEnd = "10:00"
	p.VisitsPerDay = 1
	p.ScanProbability = 100
	if _, err = s.UpdateSettings("companion", p); err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureDay("companion", clock, time.UTC); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Hour)
	record, err := s.ClaimDue("companion", clock, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	index := 3
	record, err = s.PrepareDelivery("companion", record.ID, record.Revision, Result{Status: "ready", Content: "A public finding", Sources: []Source{{Title: "Source", URL: "https://example.com/article"}}, SearchCount: 1, ReadCount: 1, ModelCalls: 1, SessionID: "topic-session", MessageIndex: &index})
	if err != nil || record.Status != "delivering" {
		t.Fatalf("topic delivery was exposed before transcript commit: %+v %v", record, err)
	}
	visible, err := s.Records("companion")
	if err != nil || len(visible) != 1 || visible[0].Content != "" || len(visible[0].Sources) != 0 || visible[0].SessionID != "" || visible[0].MessageIndex != nil {
		t.Fatalf("prepared topic leaked through the inbox: %+v %v", visible, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	records, err := s.Records("companion")
	if err != nil || len(records) != 1 || records[0].Status != "interrupted" || records[0].SessionID != "" || records[0].MessageIndex != nil || records[0].Content != "" {
		t.Fatalf("prepared topic survived restart as visible: %+v %v", records, err)
	}
}
