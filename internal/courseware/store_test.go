package courseware

import (
	"archive/zip"
	"bytes"
	"testing"
)

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
	read, text, source, err := store.Read("companion", doc.ID, "邻域", 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if read.Name != "limits.md" || text == "" || source.DocumentID != doc.ID || source.StartLine < 1 || source.End <= source.Start {
		t.Fatalf("unexpected read: %+v %q %+v", read, text, source)
	}
}

func TestPPTXSourceRefKeepsSlideLocation(t *testing.T) {
	var payload bytes.Buffer
	writer := zip.NewWriter(&payload)
	for name, text := range map[string]string{
		"ppt/slides/slide2.xml": `<p:sld xmlns:p="p" xmlns:a="a"><a:t>第二页内容</a:t></p:sld>`,
		"ppt/slides/slide1.xml": `<p:sld xmlns:p="p" xmlns:a="a"><a:t>第一页定义</a:t></p:sld>`,
	} {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = entry.Write([]byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	doc, err := store.Add("companion", "lesson.pptx", "application/vnd.openxmlformats-officedocument.presentationml.presentation", payload.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if doc.Locator != "slide" {
		t.Fatalf("locator = %q", doc.Locator)
	}
	_, excerpt, source, err := store.Read("companion", doc.ID, "第二页", 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if source.Locator != "slide" || source.StartPage != 1 || source.EndPage != 2 || !bytes.Contains([]byte(excerpt), []byte("[幻灯片 2]")) {
		t.Fatalf("source = %+v excerpt=%q", source, excerpt)
	}
}
