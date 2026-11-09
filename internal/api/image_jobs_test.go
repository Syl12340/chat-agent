package api

import (
	"context"
	"errors"
	"os"
	"testing"

	"pocket-agent/internal/imagegen"
	"pocket-agent/internal/runlog"
)

type cancelRecorder struct {
	imagegen.Generator
	imagegen.JobBackend
	ids []string
}

func (c *cancelRecorder) CancelJob(_ context.Context, id string) error {
	c.ids = append(c.ids, id)
	return nil
}

func TestImageJobScopedCancellation(t *testing.T) {
	registry, err := runlog.OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	backend := &cancelRecorder{}
	id := imageJobID("session", "request")
	s := &Server{runtime: registry, imagegen: backend, activeImageJob: imageWork{session: "session", id: id}}
	record := imageJobRecord{
		imageJob:      imageJob{ID: id, RendererID: "renderer-job", Status: "running"},
		RequestDigest: "1234567890123456789012345678901234567890123456789012345678901234",
	}
	if err := s.saveImageJob("session", record); err != nil {
		t.Fatal(err)
	}
	if _, err := s.lookupImageJob(context.Background(), "other-session", id, true); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cross-session cancellation must be rejected: %v", err)
	}
	for range 2 {
		job, err := s.lookupImageJob(context.Background(), "session", id, true)
		if err != nil || job.Status != "cancel_requested" || !job.CancelForwarded {
			t.Fatalf("cancellation receipt: %+v %v", job, err)
		}
	}
	if len(backend.ids) != 1 || backend.ids[0] != "renderer-job" {
		t.Fatalf("wrong or repeated renderer cancellation: %v", backend.ids)
	}
	s.activeImageJob = imageWork{}
	job, err := s.lookupImageJob(context.Background(), "session", id, false)
	if err != nil || job.Status != "unknown" {
		t.Fatalf("interrupted job status: %+v %v", job, err)
	}
}
