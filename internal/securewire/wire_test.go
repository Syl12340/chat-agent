package securewire

import (
	"bufio"
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// One protocol test covers the encrypted HTTP stream and consequential boundaries.
func TestEncryptedStreamBoundary(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalECPrivateKey(key)
	path := filepath.Join(t.TempDir(), "signing.key")
	if err = os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := Listen("127.0.0.1:0", path)
	if err != nil {
		t.Fatal(err)
	}
	payload := strings.Repeat("encrypted SSE bytes\n", 4000)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) })}
	go server.Serve(listener)
	defer server.Close()
	dial := func(identity *ecdsa.PublicKey) (*Conn, error) {
		raw, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
		if err != nil {
			return nil, err
		}
		ok := false
		defer func() {
			if !ok {
				raw.Close()
			}
		}()
		raw.SetDeadline(time.Now().Add(3 * time.Second))
		hello, err := readFrame(raw, 104, 183)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(append([]byte(serverContext), hello[4:101]...))
		if !bytes.Equal(hello[:4], []byte("PKS1")) || int(binary.BigEndian.Uint16(hello[101:103])) != len(hello)-103 || !ecdsa.VerifyASN1(identity, digest[:], hello[103:]) {
			return nil, io.ErrUnexpectedEOF
		}
		eph, _ := ecdh.P256().GenerateKey(rand.Reader)
		peer, err := ecdh.P256().NewPublicKey(hello[36:101])
		if err != nil {
			return nil, err
		}
		secret, err := eph.ECDH(peer)
		if err != nil {
			return nil, err
		}
		tx, rx, err := derive(secret, hello[4:36], hello[36:101], eph.PublicKey().Bytes())
		if err != nil {
			return nil, err
		}
		if err = writeFrame(raw, append([]byte("PKC1"), eph.PublicKey().Bytes()...)); err != nil {
			return nil, err
		}
		c := &Conn{Conn: raw, tx: tx, rx: rx}
		c.once.Do(func() {})
		c.ready.Store(true)
		ok = true
		return c, nil
	}
	client, err := dial(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", "http://localhost/test", nil)
	req.Close = true
	if err = req.Write(client); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(client), req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	client.Close()
	if err != nil || string(body) != payload {
		t.Fatalf("stream corrupted: %v", err)
	}
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if c, err := dial(&other.PublicKey); err == nil {
		c.Close()
		t.Fatal("wrong pinned identity accepted")
	}

	makePair := func() (*records, *records) {
		a, _, _ := derive(bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32), []byte("server"), []byte("client"))
		b, _, _ := derive(bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32), []byte("server"), []byte("client"))
		return a, b
	}
	tx, rx := makePair()
	var packet bytes.Buffer
	if err := tx.write(&packet, 1, []byte("secret")); err != nil {
		t.Fatal(err)
	}
	encoded := append([]byte{}, packet.Bytes()...)
	plain, err := rx.read(bytes.NewReader(encoded))
	if err != nil || string(plain) != "secret" {
		t.Fatal("record mismatch")
	}
	if _, err := rx.read(bytes.NewReader(encoded)); err == nil {
		t.Fatal("replayed record accepted")
	}
	_, rx = makePair()
	tampered := append([]byte{}, encoded...)
	tampered[len(tampered)-1] ^= 1
	if _, err := rx.read(bytes.NewReader(tampered)); err == nil {
		t.Fatal("tampered record accepted")
	}
	_, rx = makePair()
	if _, err := rx.read(bytes.NewReader(encoded[:len(encoded)-1])); err == nil {
		t.Fatal("truncated record accepted")
	}
	_, opposite, _ := derive(bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32), []byte("server"), []byte("client"))
	if _, err := opposite.read(bytes.NewReader(encoded)); err == nil {
		t.Fatal("reflected record accepted")
	}
	if _, err := readFrame(bytes.NewReader([]byte{255, 255, 255, 255}), 25, maxData+25); err == nil {
		t.Fatal("unbounded frame accepted")
	}
}
