package courseware

import "testing"

func TestStoreKeepsCoursewareRoleScopedAndReadable(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	doc, err := store.Add("companion", "limits.md", "text/markdown", []byte("# 极限\n先理解邻域，再使用定义。"))
	if err != nil {
		t.Fatal(err)
	}
	if docs, err := store.List("planner"); err != nil || len(docs) != 0 {
		t.Fatalf("courseware crossed roles: %+v %v", docs, err)
	}
	read, text, err := store.Read("companion", doc.ID, "邻域", 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if read.Name != "limits.md" || text == "" {
		t.Fatalf("unexpected read: %+v %q", read, text)
	}
}
