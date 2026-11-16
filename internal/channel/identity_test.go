package channel

import (
	"errors"
	"testing"
	"time"
)

func TestIdentityLinkCodeIsSingleUse(t *testing.T) {
	store, err := OpenIdentityStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	plain, _, err := store.Issue("owner", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := store.Consume(plain, "qq-official", "app-1", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if identity.PrincipalID != "owner" {
		t.Fatalf("principal = %q", identity.PrincipalID)
	}
	if _, err := store.Consume(plain, "qq-official", "app-1", "user-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("replayed code error = %v, want ErrNotFound", err)
	}
	resolved, ok := store.Resolve("qq-official", "app-1", "user-1")
	if !ok || resolved.ID != identity.ID {
		t.Fatal("bound identity was not persisted")
	}
	revoked, err := store.Revoke(identity.ID)
	if err != nil || revoked.ID != identity.ID {
		t.Fatalf("revoke = %+v, %v", revoked, err)
	}
	if _, ok := store.Resolve("qq-official", "app-1", "user-1"); ok {
		t.Fatal("revoked identity still resolved")
	}
	if store.HasAccount("qq-official", "app-1") {
		t.Fatal("revoked account still authorized deliveries")
	}
	if _, err := store.Revoke(identity.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second revoke error = %v", err)
	}
}
