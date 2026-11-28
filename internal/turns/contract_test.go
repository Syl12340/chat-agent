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
	structured := ToolEvent{Name: "workspace_share", Output: `{"kind":"workspace_file","name":"report.pdf","bytes":12,"sha256":"abc123","download_url":"/api/sessions/s1/workspace-files/abc123?path=out%2Freport.pdf"}`}
	got := artifactsOf("s1", structured)
	if len(got) != 1 || got[0].Ref != "report.pdf" || got[0].Kind != "file" || got[0].SHA256 != "abc123" {
		t.Fatalf("structured output was not turned into an artifact: %+v", got)
	}
	if got[0].Download != "/api/sessions/s1/workspace-files/abc123?path=out%2Freport.pdf" {
		t.Fatalf("the host's own download route was not kept: %+v", got[0])
	}

	prose := ToolEvent{Name: "workspace_share", Output: "已保存到 out/report.pdf"}
	if got := artifactsOf("s1", prose); got != nil {
		t.Fatalf("prose must not invent an artifact: %+v", got)
	}

	unrelated := ToolEvent{Name: "web_search", Output: `{"path":"nope"}`}
	if got := artifactsOf("s1", unrelated); got != nil {
		t.Fatalf("a read-only tool must not produce artifacts: %+v", got)
	}

	// The identifier an image tool reports maps onto this conversation's route. The
	// route is per-session, so a link built without one would be a 404 at best.
	image := ToolEvent{Name: "image_generate", Output: `{"image_id":"7f3a","profile":"oil","seed":7}`}
	if got := artifactsOf("s1", image); len(got) != 1 || got[0].Kind != "image" || got[0].Download != "/api/sessions/s1/images/7f3a" {
		t.Fatalf("image identifier should map to a session-scoped route: %+v", got)
	}

	// An absolute URL belongs to somebody else. Showing it would hand the user a
	// button that cannot authenticate, so the artifact keeps its identity only.
	foreign := ToolEvent{Name: "image_generate", Output: `{"image_id":"7f3a","url":"https://elsewhere.example/x.png"}`}
	if got := artifactsOf("s1", foreign); len(got) != 1 || got[0].Download != "/api/sessions/s1/images/7f3a" {
		t.Fatalf("a foreign URL must not become the download link: %+v", got)
	}

	// A written file can be linked because the digest it reported is what the
	// download route verifies.
	written := ToolEvent{Name: "workspace_write", Output: `{"path":"notes/a b.md","bytes":3,"sha256":"dd","status":"written"}`}
	if got := artifactsOf("s1", written); len(got) != 1 || got[0].Download != "/api/sessions/s1/workspace-files/dd?path=notes%2Fa+b.md" {
		t.Fatalf("a written file should derive its own download route: %+v", got)
	}

	// A share that reports no digest still survives, just without a link.
	bare := ToolEvent{Name: "workspace_share", Output: `{"name":"report.pdf"}`}
	if got := artifactsOf("s1", bare); len(got) != 1 || got[0].Download != "" {
		t.Fatalf("an artifact must survive even when no link can be derived: %+v", got)
	}
}
