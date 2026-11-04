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
	p, _ := s.life.Settings("companion")
	p.QuietStart = "00:00"
	p.QuietEnd = "00:00"
	p.DailyLimit = 5
	if _, err = s.life.SetSettings("companion", p); err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	run := func(id string, offset time.Duration) topics.Result {
		t.Helper()
		if err := s.life.ReserveTopicAttempt("companion", id, base.Add(offset), s.location); err != nil {
			t.Fatal(err)
		}
		return s.discoverTopic(context.Background(), topics.Record{ID: id, RoleID: "companion", Query: "astronomy"})
	}
	first := run("funnel-one", 0)
	if first.Status != "ready" || first.SearchCount != 1 || first.ReadCount != 2 || first.ModelCalls != 1 || source.searches != 1 || source.reads != 2 || model.calls != 1 || len(first.Sources) != 1 || !strings.HasSuffix(first.Sources[0].URL, "article1") {
		t.Fatalf("unbounded or invalid funnel: %+v", first)
	}
	model.response = `{"share":false}`
	second := run("funnel-two", 4*time.Hour)
	if second.Status != "silent" || second.Content != "" || len(second.Sources) != 0 {
		t.Fatalf("zero-output decision was published: %+v", second)
	}
	source.unreadable = true
	third := run("funnel-three", 8*time.Hour)
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
	fourth := run("funnel-four", 12*time.Hour)
	if fourth.Status != "silent" || fourth.Content != "" {
		t.Fatal("a new care pause did not suppress publication")
	}
}
