package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"chat-agent/internal/probe"
)

type careCountModel struct{ calls atomic.Int32 }

func (m *careCountModel) Stream(context.Context, probe.ModelRequest, func(probe.Event)) (probe.Message, error) {
	m.calls.Add(1)
	return probe.Message{Role: "assistant", Content: "fixture"}, nil
}

type careCountRank struct{ calls atomic.Int32 }

func (r *careCountRank) Rerank(string, []string, int) ([]int, bool) {
	r.calls.Add(1)
	return nil, false
}

func TestCareSkipAvoidsModelAndRerank(t *testing.T) {
	model := &careCountModel{}
	rank := &careCountRank{}
	c := probe.DefaultConfig()
	c.Model = "fixture"
	c.Provider = model
	token := "care-gate-fixture-access-token-over-32"
	s, err := New(Config{Agent: c, DataDir: t.TempDir(), AccessToken: token})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	s.memory.SetReranker(rank)
	for _, content := range []string{"用户喜欢聊天", "用户喜欢喝茶"} {
		if _, err = s.memory.Create("companion", content, "approved", "manual", ""); err != nil {
			t.Fatal(err)
		}
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		out := httptest.NewRecorder()
		s.Handler().ServeHTTP(out, req)
		if out.Code != 200 {
			t.Fatalf("%s %s: %d %s", method, path, out.Code, out.Body.String())
		}
		return out
	}
	request("PATCH", "/api/care/settings", `{"revision":0,"enabled":true,"auto_generate":true,"auto_probability":0,"daily_time":"00:00","quiet_start":"00:00","quiet_end":"00:00"}`)
	first := request("POST", "/api/care/check", `{}`)
	var skipped struct {
		Generated bool   `json:"generated"`
		Reason    string `json:"reason"`
	}
	if err = json.Unmarshal(first.Body.Bytes(), &skipped); err != nil || skipped.Generated || skipped.Reason != "random_skip" {
		t.Fatalf("unexpected skip: %s %v", first.Body.String(), err)
	}
	request("POST", "/api/care/check", `{}`)
	if model.calls.Load() != 0 || rank.calls.Load() != 0 {
		t.Fatal("skipped care called a model or reranker")
	}
	items, err := s.life.Care("companion")
	if err != nil || len(items) != 1 || items[0].Status != "skipped" || items[0].AttemptedAt != nil {
		t.Fatalf("missing durable silence decision: %+v %v", items, err)
	}
}
