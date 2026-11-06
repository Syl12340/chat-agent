package turns

import (
	"errors"
	"pocket-agent/internal/media"
	"strings"
	"testing"
)

func TestMultimodalQueueLifecycle(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	image := media.Image{ID: "0123456789abcdef0123456789abcdef", MIMEType: "image/png", Size: 4, Width: 1, Height: 1}
	_, _, err = s.EnqueueWithOptions("session", "first", "", "auto", "ecnu-max", "high", []media.Image{image})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.EnqueueWithOptions("session", "first", "", "auto", "ecnu-max", "ultra", []media.Image{image}); !errors.Is(err, ErrConflict) {
		t.Fatal("different options reused ID")
	}
	if _, duplicate, err := s.EnqueueWithOptions("session", "first", "", "auto", "ecnu-max", "high", []media.Image{image}); err != nil || !duplicate {
		t.Fatal("same options not idempotent")
	}
	withHash := image
	withHash.SHA256 = strings.Repeat("a", 64)
	if _, duplicate, err := s.EnqueueWithOptions("session", "first", "", "auto", "ecnu-max", "high", []media.Image{withHash}); err != nil || !duplicate {
		t.Fatal("adding an immutable image hash broke an old request identity")
	}
	if _, _, err = s.EnqueueWithOptions("session", "second", "next", "auto", "ecnu-plus", "high", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.EnqueueWithOptions("session", "third", "last", "auto", "ecnu-plus", "high", nil); err != nil {
		t.Fatal(err)
	}
	batch, err := s.Begin("session", 0, func(Turn) bool { return true })
	if err != nil || len(batch) != 1 {
		t.Fatal("different model was merged")
	}
	batch[0].Images[0].ID = "mutated"
	if err = s.Finish("session", "first", "completed", "reply", "", 2, nil); err != nil {
		t.Fatal(err)
	}
	batch, err = s.Begin("session", 2, func(Turn) bool { return true })
	if err != nil || len(batch) != 2 {
		t.Fatal("matching selections were not merged")
	}
	if err = s.Finish("session", "second", "completed", "reply", "", 5, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.RemoveBatch("session", "third"); err != nil {
		t.Fatal(err)
	}
	items, _, err := s.List("session")
	if err != nil || len(items) != 1 || items[0].Images[0].ID != image.ID {
		t.Fatal("batch deletion or cloning failed")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	items, _, err = s.List("session")
	if err != nil || len(items) != 1 || items[0].Model != "ecnu-max" || items[0].Thinking != "high" {
		t.Fatal("selection did not survive reopen")
	}
	images, err := s.AllImages("session")
	if err != nil || len(images) != 1 || images[0].ID != image.ID {
		t.Fatal("full image reference list failed")
	}
}
