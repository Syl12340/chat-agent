package probe

import "testing"

func TestDropTailPreservesSummary(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenSession(dir, AgentID, "summary-delete")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindRole("companion"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err = s.append(Message{Role: "user", Content: "fixture"}, Message{Role: "assistant", Content: "answer"}); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.commit(sessionRecord{Version: 1, Summary: &Summary{Content: "earlier fixture", Covered: 2}}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DropTail(2); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenSession(dir, AgentID, "summary-delete")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(s.Messages()) != 4 || s.Summary() == nil || s.Summary().Covered != 2 {
		t.Fatal("rewritten journal lost surviving history/summary")
	}
}
