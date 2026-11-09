package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"chat-agent/internal/probe"
	"chat-agent/internal/turns"
)

func TestDurableTurnQueue(t *testing.T) {
	const token = "durable-queue-fixture-token-over-32-characters"
	var calls atomic.Int32
	started := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Messages []probe.Message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		last := body.Messages[len(body.Messages)-1]
		w.Header().Set("Content-Type", "text/event-stream")
		if last.Content == "slow" {
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"未完成内容\"}}]}\n\n")
			w.(http.Flusher).Flush()
			started <- struct{}{}
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"好的。\\n\\n我们接着聊。\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	c := probe.DefaultConfig()
	c.BaseURL = upstream.URL
	c.Model = "queue-fixture"
	c.APIKey = "fixture"
	cfg := Config{Agent: c, DataDir: t.TempDir(), AccessToken: token}
	var app *Server
	var srv *httptest.Server
	start := func() {
		var err error
		app, err = New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		srv = httptest.NewServer(app.Handler())
	}
	start()
	t.Cleanup(func() { srv.Close(); app.Close(context.Background()) })
	request := func(method, path, body string) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, b
	}
	code, b := request("POST", "/api/sessions", `{}`)
	if code != 201 {
		t.Fatal(string(b))
	}
	var sess sessionInfo
	json.Unmarshal(b, &sess)
	base := "/api/sessions/" + sess.ID
	submit := func(id, text string, want int) {
		t.Helper()
		b, _ := json.Marshal(map[string]string{"request_id": id, "content": text, "mode": "auto"})
		code, reply := request("POST", base+"/turns", string(b))
		if code != want {
			t.Fatalf("submit: %d %s", code, reply)
		}
	}
	type history struct {
		Messages []probe.Message `json:"messages"`
		Turns    []turns.Turn    `json:"turns"`
		Active   bool            `json:"active"`
		Paused   bool            `json:"queue_paused"`
	}
	read := func() history {
		t.Helper()
		code, b := request("GET", base+"/messages", "")
		if code != 200 {
			t.Fatalf("history %d %s", code, b)
		}
		var h history
		if err := json.Unmarshal(b, &h); err != nil {
			t.Fatal(err)
		}
		return h
	}
	wait := func(predicate func(history) bool) history {
		t.Helper()
		deadline := time.Now().Add(6 * time.Second)
		for time.Now().Before(deadline) {
			h := read()
			if predicate(h) {
				return h
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("queue condition timed out")
		return history{}
	}
	request("POST", base+"/cancel", "")
	submit("one", "第一句话", 202)
	submit("two", "第二句话", 202)
	submit("one", "第一句话", 200)
	submit("one", "different", 409)
	if h := read(); h.Active || !h.Paused || calls.Load() != 0 {
		t.Fatal("paused queue called model")
	}
	request("POST", base+"/turns/resume", "")
	h := wait(func(h history) bool { return !h.Active && len(h.Turns) == 2 && h.Turns[1].Status == "completed" })
	if calls.Load() != 1 || len(h.Messages) != 3 || h.Messages[0].Content != "第一句话" || h.Messages[1].Content != "第二句话" || h.Turns[1].BatchID != "one" {
		t.Fatal("merged messages lost boundaries or duplicate call")
	}
	submit("two", "第二句话", 200)
	if calls.Load() != 1 {
		t.Fatal("repeated completed request charged again")
	}
	submit("slow", "slow", 202)
	select {
	case <-started:
	case <-time.After(4 * time.Second):
		t.Fatal("model never started")
	}
	submit("waiting", "下一条", 202)
	request("POST", base+"/cancel", "")
	h = wait(func(h history) bool {
		return !h.Active && h.Paused && len(h.Turns) == 4 && h.Turns[2].Status == "canceled"
	})
	if h.Turns[3].Status != "queued" || calls.Load() != 2 {
		t.Fatal("stop drained pending queue")
	}
	for _, m := range h.Messages {
		if m.Role == "assistant" && strings.Contains(m.Content, "未完成") {
			t.Fatal("partial answer entered model history")
		}
	}
	request("POST", base+"/turns/resume", "")
	wait(func(h history) bool { return !h.Active && h.Turns[3].Status == "completed" })
	if calls.Load() != 3 {
		t.Fatal("resume call count")
	}
	request("POST", base+"/cancel", "")
	submit("withdraw", "撤回的消息", 202)
	if code, _ := request("DELETE", base+"/turns/withdraw", ""); code != 200 {
		t.Fatal("withdraw failed")
	}
	// Simulate a persisted in-flight marker with no live request; reopening must
	// expose an interrupted result and require explicit action for queued work.
	_, _, err := app.turns.Enqueue(sess.ID, "crash", "不能自动重试", "auto")
	if err != nil {
		t.Fatal(err)
	}
	app.turns.Pause(sess.ID, false)
	if _, err = app.turns.Begin(sess.ID, len(read().Messages), func(turns.Turn) bool { return true }); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	if err = app.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	start()
	h = read()
	if !h.Paused || h.Turns[len(h.Turns)-1].Status != "interrupted" || calls.Load() != 3 {
		t.Fatal("restart replayed an uncertain call")
	}
	submit("two", "第二句话", 200)
	if calls.Load() != 3 {
		t.Fatal("restart lost request ledger")
	}
}
