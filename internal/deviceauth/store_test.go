package deviceauth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
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
	if _, err := store.Challenge(device.ID); !errors.Is(err, ErrPending) {
		t.Fatalf("pending device challenge: %v", err)
	}
	device, err = store.Update(device.ID, device.Revision, "approved", nil, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if store.devices[device.ID].Admin {
		t.Fatal("approval changed the legacy admin bit")
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
	if remembered.Status != "approved" || !remembered.Admin || remembered.ID != device.ID {
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
	if err != nil || !authenticated.Admin || authenticated.ID != device.ID {
		t.Fatalf("remembered device did not authenticate: device=%+v err=%v", authenticated, err)
	}
	if _, err := store.List(device.ID, false); err != nil {
		t.Fatalf("trusted device could not manage the trust list: %v", err)
	}
	if _, err := store.Update(device.ID, device.Revision, "revoked", nil, device.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authenticate(session.Token); !errors.Is(err, ErrSession) {
		t.Fatalf("revoked session still authenticated: %v", err)
	}
	if _, err := store.List(device.ID, false); !errors.Is(err, ErrAdmin) {
		t.Fatalf("revoked device could still manage the trust list: %v", err)
	}
	if _, err := store.Challenge(device.ID); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked device challenge: %v", err)
	}
}

func TestResolveAndMergeExistingDeviceCredentials(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	enroll := func(name string) (Device, *ecdsa.PrivateKey) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		item, err := store.Register(base64.StdEncoding.EncodeToString(der), name)
		if err != nil {
			t.Fatal(err)
		}
		return item, key
	}
	app, _ := enroll("phone app")
	web, webKey := enroll("phone browser")
	app, err = store.Update(app.ID, app.Revision, "approved", nil, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if match, err := store.Resolve([]string{web.ID, app.ID}); err != nil || match != app.ID {
		t.Fatalf("resolve should select approved keys before pending keys: %q, %v", match, err)
	}
	if err := store.MergeGroups(web.ID, app.ID, web.Revision, app.Revision, app.ID, false); !errors.Is(err, ErrPending) {
		t.Fatalf("pending key was merged: %v", err)
	}
	web, err = store.Update(web.ID, web.Revision, "approved", nil, app.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := store.Challenge(web.ID)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := base64.StdEncoding.DecodeString(challenge.Payload)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	signature, err := ecdsa.SignASN1(rand.Reader, webKey, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.Verify(web.ID, challenge.ID, base64.StdEncoding.EncodeToString(signature))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MergeGroups(web.ID, app.ID, web.Revision, app.Revision, app.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authenticate(session.Token); err != nil {
		t.Fatalf("merge broke existing credential: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	items, err := store.List(app.ID, false)
	if err != nil || len(items) != 2 || items[0].GroupID != app.ID || items[1].GroupID != app.ID {
		t.Fatalf("group was not remembered: %+v, %v", items, err)
	}
	if _, err := store.Update(app.ID, app.Revision, "revoked", nil, "", true); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{app.ID, web.ID} {
		if _, err := store.Challenge(id); !errors.Is(err, ErrRevoked) {
			t.Fatalf("group revocation left a credential active: %v", err)
		}
	}
}
