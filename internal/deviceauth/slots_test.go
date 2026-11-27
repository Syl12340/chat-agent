package deviceauth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newSlotTestKey makes a syntactically valid credential. This test only cares
// about identifiers and file consistency, never about verifying a signature.
func newSlotTestKey(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(der)
}

// A crash between the two writes, or a rollback that failed, leaves a slot
// pointing at a credential that is gone. The slot is only a derived mapping that
// the client rebinds on its next registration, so the store must still open:
// refusing to would keep the service down until someone edited the file by hand.
func TestOpenDropsSlotWhoseCredentialIsMissing(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// One real device so devices.json exists and is valid in its own right.
	if _, err := store.RegisterDevice(newSlotTestKey(t), "laptop", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// The half-written state: a slot naming a credential this instance never
	// stored. Both sides are well-formed - only the reference dangles.
	danglingSlot := strings.Repeat("a", 64)
	missingDevice := strings.Repeat("b", 64)
	slotsPath := filepath.Join(dir, "device-slots.json")
	body := `{"` + danglingSlot + `":"` + missingDevice + `"}`
	if err := os.WriteFile(slotsPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("a dangling slot must not block startup: %v", err)
	}
	defer reopened.Close()
	if _, ok := reopened.slots[danglingSlot]; ok {
		t.Fatal("the dangling slot should have been dropped")
	}
}
