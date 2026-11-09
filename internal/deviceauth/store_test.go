package deviceauth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"testing"
)

func TestApprovedDeviceSurvivesRestartAndAuthenticates(t *testing.T) {
	dir := t.TempDir()
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&private.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := base64.StdEncoding.EncodeToString(der)

	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	device, err := store.Register(publicKey, "fixture phone")
	if err != nil {
		t.Fatal(err)
	}
	device, err = store.Update(device.ID, device.Revision, "approved", nil, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	remembered, err := store.Register(publicKey, "fixture phone")
	if err != nil {
		t.Fatal(err)
	}
	if remembered.Status != "approved" || remembered.ID != device.ID {
		t.Fatalf("approved device was not remembered after restart: %+v", remembered)
	}
	challenge, err := store.Challenge(device.ID)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := base64.StdEncoding.DecodeString(challenge.Payload)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	signature, err := ecdsa.SignASN1(rand.Reader, private, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.Verify(device.ID, challenge.ID, base64.StdEncoding.EncodeToString(signature))
	if err != nil {
		t.Fatal(err)
	}
	authenticated, err := store.Authenticate(session.Token)
	if err != nil || authenticated.ID != device.ID {
		t.Fatalf("remembered device did not authenticate: device=%+v err=%v", authenticated, err)
	}
}
