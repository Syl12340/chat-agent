package media

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// One lifecycle covers normalized storage, scope, cleanup and restart without
// external images, a model call, or a format/size test matrix.
func TestImageLifecycle(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	picture := image.NewNRGBA(image.Rect(0, 0, 8, 6))
	picture.Set(2, 3, color.NRGBA{R: 120, G: 50, B: 30, A: 100})
	var pngBytes, jpegBytes bytes.Buffer
	if err = png.Encode(&pngBytes, picture); err != nil {
		t.Fatal(err)
	}
	if err = jpeg.Encode(&jpegBytes, picture, nil); err != nil {
		t.Fatal(err)
	}
	one, err := s.Upload("companion", "session-one", bytes.NewReader(pngBytes.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	two, err := s.Upload("companion", "session-one", bytes.NewReader(jpegBytes.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if one.MIMEType != "image/png" || two.MIMEType != "image/jpeg" || one.Width != 8 || one.Height != 6 || one.ID == two.ID || !ValidSHA256(one.SHA256) || !ValidSHA256(two.SHA256) {
		t.Fatalf("invalid metadata: %+v %+v", one, two)
	}
	read, data, err := s.Get("companion", "session-one", one.ID)
	if err != nil || read != one || read.Size != len(data) {
		t.Fatalf("roundtrip: %+v %v", read, err)
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, alpha := decoded.At(2, 3).RGBA()
	if alpha == 0 || alpha == 65535 {
		t.Fatal("PNG transparency was lost")
	}
	for _, role := range []string{"planner", "other-role"} {
		if _, _, err = s.Get(role, "session-one", one.ID); !errors.Is(err, ErrScope) {
			t.Fatal("cross-role read allowed", err)
		}
		if err = s.Prune(role, "session-one", nil); !errors.Is(err, ErrScope) {
			t.Fatal("cross-role prune allowed", err)
		}
	}
	if _, _, err = s.Get("companion", "other-session", one.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-session read allowed", err)
	}
	if _, err = s.Upload("companion", "session-one", bytes.NewBufferString("<svg xmlns=\"http://www.w3.org/2000/svg\"/>")); !errors.Is(err, ErrInvalid) {
		t.Fatal("SVG was accepted", err)
	}
	if _, err = s.Upload("companion", "../outside", bytes.NewReader(pngBytes.Bytes())); !errors.Is(err, ErrInvalid) {
		t.Fatal("path traversal was accepted", err)
	}
	one.DataURL = "must-never-enter-journal"
	encoded, _ := json.Marshal(one)
	if bytes.Contains(encoded, []byte(one.DataURL)) {
		t.Fatal("ephemeral data URL was serialized")
	}
	if err = s.Prune("companion", "session-one", map[string]bool{one.ID: true}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Get("companion", "session-one", two.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("pruned object still readable", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Get("companion", "session-one", one.ID); err != nil {
		t.Fatal("kept object did not survive restart", err)
	}
	if err = s.Delete("companion", "session-one", one.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Get("companion", "session-one", one.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted object still readable", err)
	}
	if err = s.DeleteSession("session-one"); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteSession("session-one"); err != nil {
		t.Fatal("session cleanup is not idempotent", err)
	}
}
