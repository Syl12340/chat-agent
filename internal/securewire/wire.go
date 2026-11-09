// Package securewire implements ChatWire v1: a pinned server identity,
// ephemeral P-256 ECDH and sequenced AES-256-GCM records over TCP.
// HTTP is carried inside the encrypted stream; existing Bearer auth still applies.
package securewire

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const maxData = 16 << 10
const maxRecords = 1 << 32 // At most 2^32 records per direction and session.
const serverContext = "ChatWire-v1/server"
const sessionContext = "ChatWire-v1/session"

type listener struct {
	net.Listener
	key   *ecdsa.PrivateKey
	slots chan struct{}
}

func Listen(address, keyFile string) (net.Listener, error) {
	data, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, errors.New("cannot read transport signing key")
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("transport key must be PEM")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil || key.Curve != elliptic.P256() {
		return nil, errors.New("transport key must be a P-256 EC private key")
	}
	l, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	return &listener{Listener: l, key: key, slots: make(chan struct{}, 48)}, nil
}

func (l *listener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.slots <- struct{}{}:
			return &Conn{Conn: conn, signer: l.key, release: func() { <-l.slots }}, nil
		default:
			_ = conn.Close()
		}
	}
}

// Conn authenticates on its first Read/Write. net/http gives each accepted
// connection its own goroutine and enforces the request-header deadline.
type Conn struct {
	net.Conn
	signer            *ecdsa.PrivateKey
	once              sync.Once
	handshakeErr      error
	ready             atomic.Bool
	closed            atomic.Bool
	closeOnce         sync.Once
	release           func()
	readMu, writeMu   sync.Mutex
	rx, tx            *records
	pending           []byte
	readErr, writeErr error
}

func (c *Conn) handshake() error {
	c.once.Do(func() { c.handshakeErr = c.serverHandshake() })
	return c.handshakeErr
}

func (c *Conn) serverHandshake() error {
	_ = c.Conn.SetDeadline(time.Now().Add(5 * time.Second))
	eph, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	nonce := make([]byte, 32)
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	pub := eph.PublicKey().Bytes()
	message := append(append([]byte(serverContext), nonce...), pub...)
	digest := sha256.Sum256(message)
	signature, err := ecdsa.SignASN1(rand.Reader, c.signer, digest[:])
	if err != nil {
		return err
	}
	hello := append(append(append([]byte("PKS1"), nonce...), pub...), byte(len(signature)>>8), byte(len(signature)))
	hello = append(hello, signature...)
	if err = writeFrame(c.Conn, hello); err != nil {
		return err
	}
	client, err := readFrame(c.Conn, 69, 69)
	if err != nil {
		return err
	}
	if !bytes.Equal(client[:4], []byte("PKC1")) {
		return errors.New("invalid client hello")
	}
	peer, err := ecdh.P256().NewPublicKey(client[4:])
	if err != nil {
		return err
	}
	secret, err := eph.ECDH(peer)
	if err != nil {
		return err
	}
	c2s, s2c, err := derive(secret, nonce, pub, client[4:])
	if err != nil {
		return err
	}
	c.rx, c.tx = c2s, s2c
	c.ready.Store(true)
	// Keep the five-second handshake bound separate from the HTTP deadlines.
	_ = c.Conn.SetDeadline(time.Time{})
	_ = c.Conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	return nil
}

type records struct {
	aead      cipher.AEAD
	prefix    [4]byte
	context   [32]byte
	sequence  uint64
	direction byte
	ended     bool
}

func derive(secret, challenge, serverPub, clientPub []byte) (*records, *records, error) {
	transcript := append(append(append([]byte(sessionContext), challenge...), serverPub...), clientPub...)
	digest := sha256.Sum256(transcript)
	keys, err := hkdf.Key(sha256.New, secret, challenge, string(digest[:]), 72)
	if err != nil {
		return nil, nil, err
	}
	makeRecords := func(key, prefix []byte, direction byte) *records {
		block, _ := aes.NewCipher(key)
		aead, _ := cipher.NewGCM(block)
		r := &records{aead: aead, context: digest, direction: direction}
		copy(r.prefix[:], prefix)
		return r
	}
	return makeRecords(keys[:32], keys[64:68], 0), makeRecords(keys[32:64], keys[68:72], 1), nil
}

func (r *records) nonce() []byte {
	n := make([]byte, 12)
	copy(n, r.prefix[:])
	binary.BigEndian.PutUint64(n[4:], r.sequence)
	return n
}
func (r *records) aad(length uint32) []byte {
	a := append([]byte{}, r.context[:]...)
	a = append(a, r.direction)
	var header [12]byte
	binary.BigEndian.PutUint32(header[:4], length)
	binary.BigEndian.PutUint64(header[4:], r.sequence)
	return append(a, header[:]...)
}
func (r *records) write(w io.Writer, kind byte, payload []byte) error {
	if r.ended || r.sequence >= maxRecords || len(payload) > maxData {
		return errors.New("record limit exceeded")
	}
	plain := append([]byte{kind}, payload...)
	length := uint32(8 + len(plain) + r.aead.Overhead())
	packet := make([]byte, 8)
	binary.BigEndian.PutUint64(packet, r.sequence)
	packet = r.aead.Seal(packet, r.nonce(), plain, r.aad(length))
	if err := writeFrame(w, packet); err != nil {
		return err
	}
	r.sequence++
	if kind == 2 {
		r.ended = true
	}
	return nil
}
func (r *records) read(reader io.Reader) ([]byte, error) {
	if r.ended {
		return nil, io.EOF
	}
	if r.sequence >= maxRecords {
		return nil, errors.New("record limit exceeded")
	}
	packet, err := readFrame(reader, 25, maxData+25)
	if err != nil {
		return nil, io.ErrUnexpectedEOF
	}
	if binary.BigEndian.Uint64(packet[:8]) != r.sequence {
		return nil, errors.New("replayed or out-of-order record")
	}
	plain, err := r.aead.Open(nil, r.nonce(), packet[8:], r.aad(uint32(len(packet))))
	if err != nil {
		return nil, errors.New("record authentication failed")
	}
	r.sequence++
	if plain[0] == 2 && len(plain) == 1 {
		r.ended = true
		return nil, io.EOF
	}
	if plain[0] != 1 || len(plain) < 2 {
		return nil, errors.New("invalid record kind")
	}
	return plain[1:], nil
}

func (c *Conn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if c.readErr != nil {
		return 0, c.readErr
	}
	if err := c.handshake(); err != nil {
		return 0, err
	}
	if len(c.pending) == 0 {
		var err error
		c.pending, err = c.rx.read(c.Conn)
		if err != nil {
			c.readErr = err
			return 0, err
		}
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}
func (c *Conn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	if err := c.handshake(); err != nil {
		return 0, err
	}
	total := 0
	for len(p) > 0 {
		n := min(len(p), maxData)
		if err := c.tx.write(c.Conn, 1, p[:n]); err != nil {
			c.writeErr = err
			return total, err
		}
		total += n
		p = p[n:]
	}
	return total, nil
}
func (c *Conn) Close() error {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		if c.ready.Load() && c.writeMu.TryLock() {
			if c.writeErr == nil {
				_ = c.Conn.SetWriteDeadline(time.Now().Add(time.Second))
				_ = c.tx.write(c.Conn, 2, nil)
			}
			c.writeMu.Unlock()
		}
		_ = c.Conn.Close()
		if c.release != nil {
			c.release()
		}
	})
	return nil
}
func writeFrame(w io.Writer, payload []byte) error {
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(payload)))
	for _, part := range [][]byte{size[:], payload} {
		for len(part) > 0 {
			n, err := w.Write(part)
			if err != nil {
				return err
			}
			if n == 0 {
				return io.ErrShortWrite
			}
			part = part[n:]
		}
	}
	return nil
}
func readFrame(r io.Reader, minimum, maximum uint32) ([]byte, error) {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(size[:])
	if n < minimum || n > maximum {
		return nil, errors.New("invalid bounded frame length")
	}
	data := make([]byte, n)
	_, err := io.ReadFull(r, data)
	return data, err
}
