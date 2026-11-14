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
}
