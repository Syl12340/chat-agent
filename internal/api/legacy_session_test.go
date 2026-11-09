package api

import (
	"fmt"
	"sync/atomic"
	"testing"
)

var legacySessionSequence atomic.Uint64

// legacySession creates a fixture that represents data written before new
// conversations were unified on companion. Production HTTP creation must not
// expose this compatibility seam.
func legacySession(t *testing.T, app *Server, role string) string {
	t.Helper()
	id := fmt.Sprintf("legacy_%d", legacySessionSequence.Add(1))
	if err := app.runtime.Create(id, role); err != nil {
		t.Fatal(err)
	}
	return id
}
