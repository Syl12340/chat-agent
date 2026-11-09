package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"chat-agent/internal/companion"
	"chat-agent/internal/memory"
	"chat-agent/internal/probe"
)

// One flow checks what a new conversation actually receives: permitted,
// relevant current records, bounded whole notes and no implicit activity write.
func TestRecallContextLifecycle(t *testing.T) {
	c := probe.DefaultConfig()
	c.Demo = true
	app, err := New(Config{Agent: c, DataDir: t.TempDir(), AccessToken: "recall-fixture-token-at-least-32-characters"})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(context.Background())
	fact, err := app.memory.Create("companion", "我喜欢天文摄影，常拍月亮。", "approved", "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct{ role, status, content string }{
		{"companion", "pending", "天文摄影 pending-secret"}, {"planner", "approved", "天文摄影 other-role-secret"}, {"companion", "approved", "无关的快递信息 unrelated-secret"},
	} {
		if _, err := app.memory.Create(fixture.role, fixture.content, fixture.status, "manual", ""); err != nil {
			t.Fatal(err)
		}
	}
	future := time.Now().Add(48 * time.Hour)
	if _, err := app.life.AddEpisode("companion", companion.EpisodeDraft{Title: "天文摄影观测", Content: "计划周末拍月亮，还没有出发。", Status: "open", EventAt: &future, SourceText: "superseded-source-secret"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.life.AddEpisode("companion", companion.EpisodeDraft{Title: "无关待办", Content: "unrelated-open-event-secret", Status: "open"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.life.AddEpisode("companion", companion.EpisodeDraft{Title: "天文摄影候选", Content: "pending-event-secret", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	a, _, err := app.activities.Create("companion", companion.ActivityDraft{Kind: "interest", Title: "天文摄影共学", Goal: "一起练习拍月亮", Progress: "看完曝光基础，下次聊快门。", IdempotencyKey: "recall-activity"})
	if err != nil {
		t.Fatal(err)
	}
	paused := "paused"
	a, err = app.activities.Patch("companion", a.ID, a.Revision, companion.ActivityPatch{Status: &paused}, nil)
	if err != nil {
		t.Fatal(err)
	}
	read := func(query string) string {
		t.Helper()
		data, err := app.recallReference("companion", query, "{}", nil, nil)
		if err != nil || len(data) > recallContextBytes || !json.Valid([]byte(data)) {
			t.Fatalf("invalid bounded reference: %v", err)
		}
		return data
	}
	data := read("天文摄影上次聊到哪里了？")
	for _, secret := range []string{"pending-secret", "other-role-secret", "unrelated-secret", "superseded-source-secret", "unrelated-open-event-secret", "pending-event-secret"} {
		if strings.Contains(data, secret) {
			t.Fatalf("irrelevant/unapproved/source data leaked: %s", secret)
		}
	}
	for _, want := range []string{fact.Content, "还没有出发", a.Progress, `"status":"paused"`} {
		if !strings.Contains(data, want) {
			t.Fatalf("missing saved reference: %s", want)
		}
	}
	tools := app.activityOpenTool("companion", []string{"只看看天文摄影的记录，不恢复活动。"}, "new-conversation")
	result, err := tools[0].Execute(context.Background(), json.RawMessage(`{"action":"list","query":"天文摄影"}`))
	if err != nil || !strings.Contains(result.Output, a.Progress) || !strings.Contains(result.Output, `"status":"paused"`) {
		t.Fatal("read-only tool did not retrieve saved activity progress")
	}
	bound, err := app.activities.Session("companion", "new-conversation")
	if err != nil || bound != nil {
		t.Fatal("read-only recall bound an activity")
	}
	current, err := app.activities.Get("companion", a.ID)
	if err != nil || current.Status != "paused" || current.Revision != a.Revision {
		t.Fatal("read-only recall changed progress/state")
	}
	fact, err = app.memory.Update("companion", fact.ID, "天文摄影改拍行星，暂时不拍月亮。", "approved", fact.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if corrected := read("天文摄影"); strings.Contains(corrected, "常拍月亮") || !strings.Contains(corrected, "改拍行星") {
		t.Fatal("correction did not replace old recall")
	}
	if err := app.memory.Delete("companion", fact.ID, fact.Revision); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read("天文摄影"), "改拍行星") {
		t.Fatal("deleted fact recalled")
	}
	for i := 0; i < 6; i++ {
		if _, err := app.memory.Create("companion", fmt.Sprintf("天文摄影长记录%d：", i)+strings.Repeat("完整记录。", 250), "approved", "manual", ""); err != nil {
			t.Fatal(err)
		}
	}
	data = read("天文摄影")
	var reference struct {
		Facts      []memory.Entry       `json:"approved_memories"`
		Events     []companion.Episode  `json:"events"`
		Activities []companion.Activity `json:"related_activities"`
	}
	if err := json.Unmarshal([]byte(data), &reference); err != nil {
		t.Fatal(err)
	}
	if len(reference.Facts) == 0 || len(reference.Facts) > 4 || len(reference.Events) > 3 || len(reference.Activities) > 2 {
		t.Fatal("reference limits or useful recall missing")
	}
	for _, e := range reference.Facts {
		if !strings.HasSuffix(e.Content, strings.Repeat("完整记录。", 250)) {
			t.Fatal("memory was silently clipped")
		}
	}
	if empty := read(""); strings.Contains(empty, "天文摄影") {
		t.Fatal("empty automatic query injected unrelated history")
	}
}

func TestContinuityRecallQuery(t *testing.T) {
	uiAnswer := `{"chat_ui_response":{"schema_version":1,"component_id":"q1","component_type":"quiz.single_choice","source":{"message_index":1,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"answer":{"option_id":"A"}}}`
	history := []probe.Message{
		{Role: "user", Content: "上周说的天文摄影计划还没有出发。"},
		{Role: "assistant", Content: "记得，是准备拍月亮。"},
		{Role: "user", Content: "嗯。"},
		{Role: "user", Content: uiAnswer},
	}
	if got := continuityRecallQuery("那后来呢？", history, 0, len(history)); got != history[0].Content {
		t.Fatalf("referential follow-up did not use recent user subject: %q", got)
	}
	if got := continuityRecallQuery("换个话题，聊咖啡豆。", history, 0, len(history)); got != "换个话题，聊咖啡豆。" {
		t.Fatalf("concrete topic change was overridden: %q", got)
	}
	if got := continuityRecallQuery("那后来呢？", history, 1, len(history)); got != "那后来呢？" {
		t.Fatalf("hidden history was reused for recall: %q", got)
	}
}
