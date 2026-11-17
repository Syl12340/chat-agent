package api

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"chat-agent/internal/courseware"
)

func TestStudySurfacePersistsExactCoursewareSource(t *testing.T) {
	root := t.TempDir()
	store, err := courseware.Open(filepath.Join(root, "courseware"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := store.Add("companion", "limits.md", "text/markdown", []byte("# 极限\n邻域定义与例题。"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, source, err := store.Read("companion", doc.ID, "", 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{config: Config{DataDir: root}, courseware: store}
	raw, _ := json.Marshal(map[string]any{
		"document_id": doc.ID, "document_revision": doc.Revision, "document_sha256": doc.SHA256,
		"source_start": source.Start, "source_end": source.End, "title": "极限学习卡",
		"summary": "先用邻域理解极限，再练习定义。", "progress": 0.25,
		"next_questions": []string{"给我一道邻域定义判断题"},
	})
	result, err := server.saveStudySurface(raw, localTaskScope{SessionID: "chat_one", RoleID: "companion", RequestID: "request_one"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, `"revision":1`) || !strings.Contains(result.Output, `"source_ref"`) {
		t.Fatalf("result = %s", result.Output)
	}
	document, err := server.surfaceStore().load("chat_one", "companion")
	if err != nil || len(document.Items) != 1 {
		t.Fatalf("surface document = %+v err=%v", document, err)
	}
	item := document.Items[0]
	if item.View.Title != "极限学习卡" || item.View.Revision != 1 || item.View.Root.Children[0].Text == "" {
		t.Fatalf("surface = %+v", item)
	}

	var created map[string]any
	_ = json.Unmarshal([]byte(result.Output), &created)
	raw, _ = json.Marshal(map[string]any{
		"surface_id": created["surface_id"], "expected_revision": 1,
		"document_id": doc.ID, "document_revision": doc.Revision, "document_sha256": doc.SHA256,
		"source_start": source.Start, "source_end": source.End, "title": "极限学习卡",
		"summary": "已理解邻域定义，下一步练习证明。", "progress": 0.6,
	})
	updated, err := server.saveStudySurface(raw, localTaskScope{SessionID: "chat_one", RoleID: "companion"})
	if err != nil || !strings.Contains(updated.Output, `"revision":2`) {
		t.Fatalf("update = %s err=%v", updated.Output, err)
	}
}
