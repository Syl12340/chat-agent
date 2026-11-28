package deviceauth

import (
	"errors"
	"strings"
	"testing"
)

// A registration proves knowledge of a public key, not possession of it. So an
// anonymous request must never be able to move a credential that is already
// trusted into a different state - including by aiming a pending slot at it,
// which would downgrade an approved device to awaiting approval.
func TestAnonymousSlotCannotDowngradeATrustedKey(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// The operator's device, already approved.
	trustedKey := newSlotTestKey(t)
	trusted, err := store.RegisterDevice(trustedKey, "phone", "")
	if err != nil {
		t.Fatal(err)
	}
	trusted, err = store.Update(trusted.ID, trusted.Revision, "approved", nil, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if trusted.Status != "approved" {
		t.Fatalf("fixture is not approved: %+v", trusted)
	}

	// An anonymous caller takes a slot with a key of their own, then points that
	// same slot at the trusted public key - which they can read, but not use.
	slot := strings.Repeat("c", 64)
	if _, err := store.RegisterDevice(newSlotTestKey(t), "intruder", slot); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterDevice(trustedKey, "intruder", slot); err == nil {
		t.Fatal("a public key was accepted as grounds to displace a trusted credential")
	}

	current, ok := store.devices[trusted.ID]
	if !ok {
		t.Fatal("the trusted device disappeared entirely")
	}
	if current.Status != "approved" {
		t.Fatalf("trusted credential changed state to %q", current.Status)
	}
	if current.PublicKey != trustedKey {
		t.Fatal("the trusted credential now holds a different key")
	}
}

// The same request must not be able to revive a revoked credential either: that
// would let an intruder undo the operator's decision to cut a device off.
func TestAnonymousSlotCannotReviveARevokedKey(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	revokedKey := newSlotTestKey(t)
	device, err := store.RegisterDevice(revokedKey, "old laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	device, err = store.Update(device.ID, device.Revision, "approved", nil, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(device.ID, device.Revision, "revoked", nil, "", true); err != nil {
		t.Fatalf("revoking failed: %v", err)
	}

	slot := strings.Repeat("d", 64)
	if _, err := store.RegisterDevice(newSlotTestKey(t), "intruder", slot); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterDevice(revokedKey, "intruder", slot); err == nil {
		t.Fatal("a revoked credential was allowed back through an anonymous slot")
	}

	current, ok := store.devices[device.ID]
	if !ok {
		t.Fatal("the revoked device disappeared")
	}
	if current.Status != "revoked" {
		t.Fatalf("revoked credential became %q", current.Status)
	}
}

// Replacing an unapproved key in the same slot must keep working: that is the
// normal case of a browser regenerating its key after clearing storage.
func TestPendingSlotStillAcceptsANewKey(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	slot := strings.Repeat("e", 64)
	first, err := store.RegisterDevice(newSlotTestKey(t), "browser", slot)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.RegisterDevice(newSlotTestKey(t), "browser", slot)
	if err != nil {
		t.Fatalf("a pending slot must still accept a replacement key: %v", err)
	}
	if first.ID == second.ID {
		t.Fatal("the replacement produced the same identifier")
	}
	if store.slots[slot] != second.ID {
		t.Fatal("the slot did not move to the new key")
	}
	if _, ok := store.devices[first.ID]; ok {
		t.Fatal("the replaced pending record should be gone")
	}
	if err := errors.Unwrap(nil); err != nil {
		t.Fatal(err)
	}
}
