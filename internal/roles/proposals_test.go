package roles

import (
	"errors"
	"testing"
)

func TestProfileProposalLifecycle(t *testing.T) {
	m, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	before, err := m.Load("companion")
	if err != nil {
		t.Fatal(err)
	}
	content := before.Profile + "\nPrefer quiet walks together.\n"
	p, err := m.ProposeProfile(before.ID, ProfileHash(before.Profile), content, "Requested lasting shared interest", "session-one")
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := m.Load(before.ID)
	if err != nil || unchanged.Profile != before.Profile || p.Status != "pending" || p.File != "PROFILE.md" {
		t.Fatalf("proposal changed profile or scope: %+v, %v", p, err)
	}
	if _, err := m.List(); err != nil {
		t.Fatalf("proposal metadata became a role: %v", err)
	}
	if _, err := m.ReviewProposal("planner", p.ID, "approve", p.Revision); !errors.Is(err, ErrProposalNotFound) {
		t.Fatalf("cross-role review: %v", err)
	}
	if _, err := m.ReviewProposal(before.ID, p.ID, "approve", p.Revision+1); !errors.Is(err, ErrProposalConflict) {
		t.Fatalf("stale revision accepted: %v", err)
	}
	p, err = m.ReviewProposal(before.ID, p.ID, "approve", p.Revision)
	if err != nil || p.Status != "applied" || p.Before != before.Profile || p.BaseHash != ProfileHash(before.Profile) {
		t.Fatalf("approval: %+v, %v", p, err)
	}
	after, err := m.Load(before.ID)
	if err != nil || after.Profile != content || after.Identity != before.Identity || after.Rules != before.Rules {
		t.Fatalf("wrong document updated: %+v, %v", after, err)
	}
	if err := m.DeleteProposal(before.ID, p.ID, p.Revision); err != nil {
		t.Fatal(err)
	}
	afterDelete, _ := m.Load(before.ID)
	if afterDelete.Profile != content {
		t.Fatal("deleting applied record reverted profile")
	}
	if _, err := m.ProposeProfile(before.ID, ProfileHash(before.Profile), content+"stale", "stale turn", "session-two"); !errors.Is(err, ErrProposalConflict) {
		t.Fatalf("stale proposal creation accepted: %v", err)
	}
	stale, err := m.ProposeProfile(before.ID, ProfileHash(content), content+"Candidate", "new request", "session-two")
	if err != nil {
		t.Fatal(err)
	}
	manual := content + "Manual edit"
	if _, err = m.Update(before.ID, "PROFILE.md", manual); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReviewProposal(before.ID, stale.ID, "approve", stale.Revision); !errors.Is(err, ErrProposalConflict) {
		t.Fatalf("manual edit overwritten: %v", err)
	}
	// Simulate atomic file replacement followed by a crash before status save.
	if _, err = m.Update(before.ID, "PROFILE.md", stale.Content); err != nil {
		t.Fatal(err)
	}
	recovered, err := m.ReviewProposal(before.ID, stale.ID, "approve", stale.Revision)
	if err != nil || recovered.Status != "applied" || recovered.Before != content {
		t.Fatalf("interrupted approval recovery: %+v, %v", recovered, err)
	}
	rejected, err := m.ProposeProfile(before.ID, ProfileHash(stale.Content), stale.Content+"Other", "declined request", "session-two")
	if err != nil {
		t.Fatal(err)
	}
	rejected, err = m.ReviewProposal(before.ID, rejected.ID, "reject", rejected.Revision)
	if err != nil || rejected.Status != "rejected" {
		t.Fatalf("rejection: %+v, %v", rejected, err)
	}
	final, _ := m.Load(before.ID)
	if final.Profile != stale.Content {
		t.Fatal("rejection changed profile")
	}
}
