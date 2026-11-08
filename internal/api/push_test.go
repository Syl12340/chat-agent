package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pocket-agent/internal/companion"
	"pocket-agent/internal/probe"
)

func pushDevice(t *testing.T, s *Server) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.devices.Register(base64.StdEncoding.EncodeToString(public), "push-fixture")
	if err != nil {
		t.Fatal(err)
	}
	d, err = s.devices.Update(d.ID, d.Revision, "approved", nil, "", true)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.devices.Challenge(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := base64.StdEncoding.DecodeString(c.Payload)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(payload)
	signature, err := ecdsa.SignASN1(rand.Reader, key, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.devices.Verify(d.ID, c.ID, base64.StdEncoding.EncodeToString(signature))
	if err != nil {
		t.Fatal(err)
	}
	return d.ID, session.Token
}

func TestPushInboxLifecycle(t *testing.T) {
	c := probe.DefaultConfig()
	c.Demo = true
	owner := "push-inbox-fixture-access-token-over-32"
	dir := t.TempDir()
	s, err := New(Config{Agent: c, DataDir: dir, AccessToken: owner})
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = s.Close(context.Background())
		}
	}()
	id, token := pushDevice(t, s)
	request := func(method, path, body, auth string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		out := httptest.NewRecorder()
		s.Handler().ServeHTTP(out, req)
		if out.Code != want {
			t.Fatalf("%s: %d %s", path, out.Code, out.Body.String())
		}
		return out
	}
	request("GET", "/api/push/inbox", "", "", 401)
	request("PATCH", "/api/push/settings", `{"enabled":true}`, owner, 403)
	request("GET", "/api/push/inbox", "", token, 409)
	request("PATCH", "/api/push/settings", `{}`, token, 400)
	request("PATCH", "/api/push/settings", `{"enabled":true}`, token, 200)
	if s.receiverOnline(time.Now()) {
		t.Fatal("opt-in alone created live receiver")
	}
	p, _ := s.life.Settings("companion")
	p.QuietStart = "00:00"
	p.QuietEnd = "00:00"
	if _, err = s.life.SetSettings("companion", p); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	due := now.Add(-time.Minute)
	reminder, err := s.tasks.Add("companion", "reminder", "fixture reminder", "push-reminder", "manual", "", &due, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.tasks.Advance(now); err != nil {
		t.Fatal(err)
	}
	var inbox pushInbox
	if err = json.Unmarshal(request("GET", "/api/push/inbox?wait=0", "", token, 200).Body.Bytes(), &inbox); err != nil {
		t.Fatal(err)
	}
	if len(inbox.Items) != 1 || inbox.Items[0].ID != reminder.ID || inbox.Items[0].Kind != "reminder" || len(inbox.Cursor) != 64 || !s.receiverOnline(now) {
		t.Fatalf("invalid inbox: %+v", inbox)
	}
	first := inbox.Cursor
	if err = json.Unmarshal(request("GET", "/api/push/inbox?wait=0&cursor="+first, "", token, 200).Body.Bytes(), &inbox); err != nil || inbox.Cursor != first {
		t.Fatal("read mutated cursor")
	}
	items, _ := s.tasks.List("companion", "reminder")
	if len(items) != 1 || items[0].Status != "due" {
		t.Fatal("read acknowledged reminder")
	}
	if s.receiverOnline(now.Add(91 * time.Second)) {
		t.Fatal("receiver lease did not expire")
	}
	request("GET", "/api/push/inbox?wait=21", "", token, 400)
	request("GET", "/api/push/inbox?cursor=bad", "", token, 400)
	if err = s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	closed = true
	s, err = New(Config{Agent: c, DataDir: dir, AccessToken: owner})
	if err != nil {
		t.Fatal(err)
	}
	closed = false
	if !s.push.get(id) || s.receiverOnline(time.Now()) {
		t.Fatal("restart lost subscription or revived live lease")
	}
	if err = s.push.set(id, false); err != nil {
		t.Fatal(err)
	}
	if s.push.touch(id, time.Now()) {
		t.Fatal("disabled receiver renewed lease")
	}
	// A suspended role is withheld without acknowledging its source records.
	p, _ = s.life.Settings("companion")
	until := time.Now().Add(time.Hour)
	p.SnoozeUntil = &until
	if p, err = s.life.SetSettings("companion", p); err != nil {
		t.Fatal(err)
	}
	quietInbox, err := s.readPushInbox(time.Now())
	if err != nil || len(quietInbox.Items) != 0 {
		t.Fatal("paused role leaked a notification")
	}
	p.SnoozeUntil = nil
	if _, err = s.life.SetSettings("companion", p); err != nil {
		t.Fatal(err)
	}
	// A revoked device must not finish an already-waiting poll with content.
	secondID, secondToken := pushDevice(t, s)
	request("PATCH", "/api/push/settings", `{"enabled":true}`, secondToken, 200)
	read := request("GET", "/api/push/inbox", "", secondToken, 200)
	if err = json.Unmarshal(read.Body.Bytes(), &inbox); err != nil {
		t.Fatal(err)
	}
	finished := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest("GET", "/api/push/inbox?wait=20&cursor="+inbox.Cursor, nil)
		req.Header.Set("Authorization", "Bearer "+secondToken)
		out := httptest.NewRecorder()
		s.Handler().ServeHTTP(out, req)
		finished <- out
	}()
	deadline := time.Now().Add(time.Second)
	for len(s.pushSlots) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	devices, _ := s.devices.List("", true)
	for _, device := range devices {
		if device.ID == secondID {
			body, _ := json.Marshal(map[string]any{"revision": device.Revision, "status": "revoked"})
			request("PATCH", "/api/devices/"+device.ID, string(body), owner, 200)
		}
	}
	select {
	case out := <-finished:
		if out.Code != 401 && out.Code != 403 {
			t.Fatalf("revoked active poll: %d %s", out.Code, out.Body.String())
		}
	case <-time.After(4 * time.Second):
		t.Fatal("revocation did not stop active poll")
	}
}

func TestBackgroundCareLease(t *testing.T) {
	model := &careCountModel{}
	c := probe.DefaultConfig()
	c.Model = "fixture"
	c.Provider = model
	s, err := New(Config{Agent: c, DataDir: t.TempDir(), AccessToken: "background-care-fixture-token-over-32"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	if err = s.runtime.Create("background-care-session", "companion"); err != nil {
		t.Fatal(err)
	}
	id, _ := pushDevice(t, s)
	now := time.Now()
	p, _ := s.life.Settings("companion")
	p.Enabled = true
	p.AutoGenerate = true
	probability := 0
	p.AutoProbability = &probability
	p.QuietStart = "00:00"
	p.QuietEnd = "00:00"
	p.DailyTime = now.In(s.location).Format("15:04")
	if _, err = s.life.SetSettings("companion", p); err != nil {
		t.Fatal(err)
	}
	if err = s.sweepBackgroundCare(now); err != nil {
		t.Fatal(err)
	}
	items, _ := s.life.Care("companion")
	if len(items) != 0 {
		t.Fatal("offline receiver started paid care plan")
	}
	if err = s.push.set(id, true); err != nil {
		t.Fatal(err)
	}
	s.push.touch(id, now)
	if err = s.sweepBackgroundCare(now); err != nil {
		t.Fatal(err)
	}
	items, _ = s.life.Care("companion")
	if len(items) != 1 || items[0].Status != "skipped" || model.calls.Load() != 0 {
		t.Fatalf("background probability gate bypassed: %+v", items)
	}
	if err = s.sweepBackgroundCare(now); err != nil {
		t.Fatal(err)
	}
	if model.calls.Load() != 0 {
		t.Fatal("silence was retried")
	}
	// Explicit followups do not pass through the optional daily dice.
	followup := now.Add(-time.Minute)
	_, err = s.life.AddEpisode("companion", companion.EpisodeDraft{Title: "约好的跟进", Content: "用户明确要求稍后关心一次", Status: "open", AllowFollowup: true, FollowupAt: &followup})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.sweepBackgroundCare(now); err != nil {
		t.Fatal(err)
	}
	if model.calls.Load() != 1 {
		t.Fatal("explicit followup was not generated once")
	}
	if err = s.push.set(id, false); err != nil {
		t.Fatal(err)
	}
	if s.receiverOnline(now) {
		t.Fatal("disabled subscription retained a live lease")
	}
}
