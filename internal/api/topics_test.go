package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"pocket-agent/internal/probe"
	"pocket-agent/internal/topics"
	"pocket-agent/internal/webtools"
)

type topicFixtureWeb struct {
	searches, reads int
	unreadable      bool
}

func (*topicFixtureWeb) SearchEnabled() bool { return true }
func (*topicFixtureWeb) FetchEnabled() bool  { return true }
func (f *topicFixtureWeb) SearchSingle(context.Context, string) ([]webtools.Result, error) {
	f.searches++
	out := []webtools.Result{}
	for i := 0; i < 8; i++ {
		out = append(out, webtools.Result{Title: fmt.Sprintf("Article%d", i), URL: fmt.Sprintf("https://source.example/article%d", i), Snippet: "snippet-only-sentinel"})
	}
	return out, nil
}
func (f *topicFixtureWeb) Fetch(_ context.Context, url string) (webtools.Page, error) {
	f.reads++
	if f.unreadable {
		return webtools.Page{}, errors.New("fixture page unavailable")
	}
	return webtools.Page{URL: url, Content: "direct-body-sentinel: the source explains a small astronomy finding"}, nil
}

type topicFixtureModel struct {
	t            *testing.T
	calls        int
	response     string
	beforeReturn func()
}

func (f *topicFixtureModel) Stream(_ context.Context, r probe.ModelRequest, _ func(probe.Event)) (probe.Message, error) {
	f.calls++
	if len(r.Tools) > 0 {
		f.t.Error("discovery acquired arbitrary tools")
	}
	if !strings.Contains(r.Messages[0].Content, "direct-body-sentinel") || strings.Contains(r.Messages[0].Content, "snippet-only-sentinel") {
		f.t.Error("model did not use directly fetched bodies")
	}
	if f.beforeReturn != nil {
		f.beforeReturn()
	}
	return probe.Message{Role: "assistant", Content: f.response}, nil
}

func TestTopicDiscoveryFunnel(t *testing.T) {
	model := &topicFixtureModel{t: t, response: `{"share":true,"text":"有个有趣的天文发现。","source_indexes":[1]}`}
	c := probe.DefaultConfig()
	c.Model = "fixture"
	c.Provider = model
	s, err := New(Config{Agent: c, DataDir: t.TempDir(), AccessToken: "topic-funnel-local-fixture-token-over-32"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	source := &topicFixtureWeb{}
	s.topicSource = source
	if err = s.runtime.Create("topic-session", "companion"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.session("topic-session"); err != nil {
		t.Fatal(err)
	}
	p, _ := s.life.Settings("companion")
	p.QuietStart = "00:00"
	p.QuietEnd = "00:00"
	p.DailyLimit = 5
	if _, err = s.life.SetSettings("companion", p); err != nil {
		t.Fatal(err)
	}
	topicSettings, _ := s.topics.Settings("companion")
	topicSettings.Enabled = true
	topicSettings.Queries = []string{"astronomy"}
	topicSettings.WindowStart = "09:00"
	topicSettings.WindowEnd = "10:00"
	topicSettings.VisitsPerDay = 1
	topicSettings.ScanProbability = 100
	if _, err = s.topics.UpdateSettings("companion", topicSettings); err != nil {
		t.Fatal(err)
	}
	base := time.Now().In(s.location)
	run := func(day int) topics.Record {
		t.Helper()
		now := time.Date(base.Year(), base.Month(), base.Day()+day, 8, 0, 0, 0, s.location)
		if err := s.topics.EnsureDay("companion", now, s.location); err != nil {
			t.Fatal(err)
		}
		records, err := s.topics.Records("companion")
		if err != nil {
			t.Fatal(err)
		}
		var due topics.Record
		for _, item := range records {
			if item.Status == "scheduled" && item.DueAt.In(s.location).Format("2006-01-02") == now.Format("2006-01-02") {
				due = item
				break
			}
		}
		claimed, err := s.topics.ClaimDue("companion", due.DueAt, s.location)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.life.ReserveTopicAttempt("companion", claimed.ID, claimed.DueAt, s.location); err != nil {
			t.Fatal(err)
		}
		result, err := s.discoverTopic(context.Background(), claimed)
		if err != nil {
			t.Fatal(err)
		}
		if result.Status == "ready" {
			if _, err = s.topics.Mark("companion", result.ID, "seen", result.Revision); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	first := run(0)
	if first.Status != "ready" || first.SearchCount != 1 || first.ReadCount != 2 || first.ModelCalls != 1 || source.searches != 1 || source.reads != 2 || model.calls != 1 || len(first.Sources) != 1 || !strings.HasSuffix(first.Sources[0].URL, "article1") {
		t.Fatalf("unbounded or invalid funnel: %+v", first)
	}
	if first.SessionID != "topic-session" || first.MessageIndex == nil {
		t.Fatalf("topic was not bound to its conversation: %+v", first)
	}
	topicSession := s.sessions["topic-session"].Messages()
	if *first.MessageIndex >= len(topicSession) || topicSession[*first.MessageIndex].Content != first.Content {
		t.Fatal("topic inbox and conversation diverged")
	}
	model.response = `{"share":false}`
	second := run(1)
	if second.Status != "silent" || second.Content != "" || len(second.Sources) != 0 {
		t.Fatalf("zero-output decision was published: %+v", second)
	}
	source.unreadable = true
	third := run(2)
	if third.Status != "silent" || third.ModelCalls != 0 || model.calls != 2 || source.reads != 6 {
		t.Fatalf("unreadable pages caused model/retry: %+v", third)
	}
	source.unreadable = false
	model.response = `{"share":true,"text":"不应发送。","source_indexes":[0]}`
	model.beforeReturn = func() {
		prefs, _ := s.life.Settings("companion")
		until := time.Now().Add(48 * time.Hour)
		prefs.SnoozeUntil = &until
		if _, err := s.life.SetSettings("companion", prefs); err != nil {
			t.Error(err)
		}
	}
	fourth := run(3)
	if fourth.Status != "silent" || fourth.Content != "" {
		t.Fatal("a new care pause did not suppress publication")
	}
}
