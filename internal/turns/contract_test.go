package turns

import (
	"testing"
	"time"

	"chat-agent/internal/probe"
)

// The four levels exist so nobody has to re-derive task state, so the projection
// has to carry what a caller actually needs: what was asked, how it ran, what it
// did, and where.
func TestContractOfProjectsAFinishedTurn(t *testing.T) {
	turn := Turn{
		ID:        "turn-1",
		Content:   "整理这五页课件",
		Status:    "completed",
		Text:      "已经整理好了。",
		Model:     "ecnu-max",
		Thinking:  "high",
		CreatedAt: time.Unix(1700000000, 0).UTC(),
		Origin:    TurnOrigin{Kind: "channel", Platform: "qq"},
		Tools: []ToolEvent{
			{ID: "call-1", Name: "workspace_read", Output: "content", View: &probe.ToolView{Endpoint: "server"}},
			{ID: "call-2", Name: "workspace_write", Error: "disk full"},
		},
	}
	task := ContractOf("session-1", turn)

	if task.ID != "turn-1" || task.Session != "session-1" || task.Request != "整理这五页课件" {
		t.Fatalf("task lost its identity: %+v", task)
	}
	if task.Status != "completed" || task.Origin != "channel" {
		t.Fatalf("task status or origin wrong: %+v", task)
	}
	if len(task.Runs) != 1 {
		t.Fatalf("want one run, got %d", len(task.Runs))
	}
	run := task.Runs[0]
	if run.Model != "ecnu-max" || run.Thinking != "high" || run.Reply == "" {
		t.Fatalf("run lost its execution detail: %+v", run)
	}
	if len(run.Operations) != 2 {
		t.Fatalf("want two operations, got %d", len(run.Operations))
	}
	if run.Operations[0].Status != "ok" || run.Operations[0].Location != "server" {
		t.Fatalf("read operation wrong: %+v", run.Operations[0])
	}
	if run.Operations[1].Status != "failed" || run.Operations[1].Error == "" {
		t.Fatalf("a failed write must be visible as failed: %+v", run.Operations[1])
	}
}

// A file only counts as a deliverable when the tool said so in a structured
// field. Prose that mentions a path is not evidence.
func TestArtifactsRequireStructuredOutput(t *testing.T) {
	structured := ToolEvent{Name: "workspace_share", Output: `{"path":"out/report.pdf","sha256":"abc123"}`}
	got := artifactsOf(structured)
	if len(got) != 1 || got[0].Ref != "out/report.pdf" || got[0].Kind != "file" || got[0].SHA256 != "abc123" {
		t.Fatalf("structured output was not turned into an artifact: %+v", got)
	}

	prose := ToolEvent{Name: "workspace_share", Output: "已保存到 out/report.pdf"}
	if got := artifactsOf(prose); got != nil {
		t.Fatalf("prose must not invent an artifact: %+v", got)
	}

	unrelated := ToolEvent{Name: "web_search", Output: `{"path":"nope"}`}
	if got := artifactsOf(unrelated); got != nil {
		t.Fatalf("a read-only tool must not produce artifacts: %+v", got)
	}

	image := ToolEvent{Name: "image_generate", Output: `{"url":"/api/images/abc"}`}
	if got := artifactsOf(image); len(got) != 1 || got[0].Kind != "image" {
		t.Fatalf("an image has its own kind: %+v", got)
	}
}
