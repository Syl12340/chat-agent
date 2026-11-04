package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCompactionPreservesHistory(t *testing.T) {
	var summaries atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []Message         `json:"messages"`
			Tools    []json.RawMessage `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.HasPrefix(req.Messages[0].Content, "Summarize") {
			if len(req.Tools) != 0 {
				t.Error("summary request must not advertise tools")
			}
			if summaries.Add(1) == 2 {
				chunk(w, map[string]string{"content": "truncated summary"}, "length")
				fmt.Fprint(w, "data: [DONE]\n\n")
				return
			}
			answer(w, "Decisions 0–3 are preserved; keep the project small.")
			return
		}
		if !strings.Contains(req.Messages[1].Content, "Decisions 0–3") {
			t.Error("summary not sent in next model request")
		}
		for _, m := range req.Messages {
			if m.Content == "Question 0" {
				t.Error("old raw context still sent after compaction")
			}
		}
		answer(w, "Next step.")
	}))
	defer upstream.Close()
	dir := t.TempDir()
	sess, err := OpenSession(dir, AgentID, "compact")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	for i := 0; i < 6; i++ {
		if err := sess.append(Message{Role: "user", Content: fmt.Sprintf("Question %d", i)}, Message{Role: "assistant", Content: fmt.Sprintf("Decision %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	c := DefaultConfig()
	c.BaseURL = upstream.URL + "/v1"
	c.APIKey = "fake"
	c.Model = "fake"
	c.KeepRecentTurns = 2
	a, err := New(c, sess)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := Compact(context.Background(), a)
	if err != nil || !changed {
		t.Fatalf("compact: %t %v", changed, err)
	}
	if len(sess.Messages()) != 12 || sess.Summary().Covered != 8 || len(sess.ContextMessages()) != 5 {
		t.Fatal("compaction changed full history or recent turns")
	}
	if err := Run(context.Background(), a, c.Timeout, "Continue", nil); err != nil {
		t.Fatal(err)
	}
	before := *sess.Summary()
	if _, err := Compact(context.Background(), a); err == nil {
		t.Fatal("truncated summary must fail")
	}
	if *sess.Summary() != before || len(sess.Messages()) != 14 {
		t.Fatal("failed summary modified context or history")
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSession(dir, AgentID, "compact")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Summary() == nil || *reopened.Summary() != before || len(reopened.Messages()) != 14 {
		t.Fatal("summary or full history not restored")
	}
}
