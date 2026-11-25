package workspace

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func sha256Of(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", sum[:])
}

// The receipt contract in runlog rejects anything whose entity ids are not
// identifier-shaped or whose revisions do not line up one-to-one, and it rejects
// silently. These assertions mirror that contract so a write cannot quietly stop
// being reconcilable after a restart.
var receiptIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var receiptDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func TestWriteProducesAReconcilableReceipt(t *testing.T) {
	plugin, root := open(t)
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("version one"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := call(t, plugin.write, `{"workspace":"server","path":"note.txt","content":"version two","expected_sha256":"missing"}`)
	if err == nil {
		t.Fatal("writing over an existing file with a missing marker must be refused")
	}

	digest := sha256Of("version one")
	body := `{"workspace":"server","path":"note.txt","content":"version two","expected_sha256":"` + digest + `"}`
	result, err = call(t, plugin.write, body)
	if err != nil {
		t.Fatal(err)
	}
	receipt := result.Receipt
	if receipt == nil {
		t.Fatal("a write must carry a business receipt or the outcome cannot be reconciled later")
	}
	if receipt.Store != "workspace" || receipt.OperationID == "" {
		t.Fatalf("receipt store/operation = %q/%q", receipt.Store, receipt.OperationID)
	}
	if !receiptDigestPattern.MatchString(receipt.ArgumentsSHA256) || !receiptDigestPattern.MatchString(receipt.OutcomeDigest) {
		t.Fatalf("receipt digests must be lowercase hex: %+v", receipt)
	}
	if len(receipt.EntityIDs) != len(receipt.ResultingRevisions) || len(receipt.EntityIDs) != 1 {
		t.Fatalf("entity ids and revisions must pair up: %+v", receipt)
	}
	if !receiptIDPattern.MatchString(receipt.EntityIDs[0]) {
		t.Fatalf("entity id %q is not identifier-shaped, the run log will drop the receipt", receipt.EntityIDs[0])
	}
	if receipt.ResultingRevisions[0] < 1 {
		t.Fatalf("revision %d must be at least 1", receipt.ResultingRevisions[0])
	}
	// The outcome digest is the digest of what was written, so it can be checked
	// against the file afterwards.
	if receipt.OutcomeDigest != sha256Of("version two") {
		t.Fatalf("outcome digest does not describe the written content")
	}
}

func TestWriteReceiptIsDeterministicForTheSameContent(t *testing.T) {
	plugin, root := open(t)
	body := func(content string) string {
		return `{"workspace":"server","path":"same.txt","content":"` + content + `","expected_sha256":"missing"}`
	}
	first, err := call(t, plugin.write, body("one"))
	if err != nil {
		t.Fatal(err)
	}
	// The file exists now, so replacing it needs the digest of what is there.
	replace := `{"workspace":"server","path":"same.txt","content":"two","expected_sha256":"` + sha256Of("one") + `"}`
	second, err := call(t, plugin.write, replace)
	if err != nil {
		t.Fatal(err)
	}
	if first.Receipt.EntityIDs[0] != second.Receipt.EntityIDs[0] {
		t.Fatal("the same path must produce the same entity id")
	}
	if first.Receipt.ResultingRevisions[0] == second.Receipt.ResultingRevisions[0] {
		t.Fatal("different content must produce a different revision")
	}
	if _, err := os.Stat(filepath.Join(root, "same.txt")); err != nil {
		t.Fatal(err)
	}
}
