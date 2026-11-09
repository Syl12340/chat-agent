// Package deviceauth stores the device whitelist and short-lived proof sessions.
package deviceauth

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"chat-agent/internal/storage"
)

var (
	ErrInvalid     = errors.New("invalid device request")
	ErrNotFound    = errors.New("device not found")
	ErrPending     = errors.New("device pending approval")
	ErrRevoked     = errors.New("device revoked")
	ErrConflict    = errors.New("device revision conflict")
	ErrLimit       = errors.New("device authentication capacity reached")
	ErrRateLimited = errors.New("device authentication rate limited")
	ErrChallenge   = errors.New("challenge invalid or expired")
	ErrSignature   = errors.New("invalid device signature")
	ErrSession     = errors.New("device session invalid or expired")
	ErrClosed      = errors.New("device store closed")
	ErrAdmin       = errors.New("device administrator required")
)

const maxDevices = 512

type Device struct {
	ID          string `json:"id"`
	Fingerprint string `json:"fingerprint"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Revision    int    `json:"revision"`
	Admin       bool   `json:"admin"`
}

type record struct {
	Device
	PublicKey string `json:"public_key"`
}
type snapshot struct {
	Version  int      `json:"version"`
	ServerID string   `json:"server_id"`
	Devices  []record `json:"devices"`
}
type Challenge struct {
	ID        string    `json:"challenge_id"`
	Payload   string    `json:"payload"`
	ExpiresAt time.Time `json:"expires_at"`
	ServerID  string    `json:"server_id"`
}
type challengeState struct {
	Challenge
	DeviceID string
	Raw      []byte
}
type Session struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	Device    Device    `json:"device"`
	ServerID  string    `json:"server_id"`
}
type sessionState struct {
	DeviceID  string
	ExpiresAt time.Time
}
type rateWindow struct {
	Start time.Time
	Count int
}
type Store struct {
	mu         sync.Mutex
	dir        string
	lockPath   string
	serverID   string
	devices    map[string]record
	challenges map[string]challengeState
	sessions   map[[32]byte]sessionState
	rates      map[string]rateWindow
	closed     bool
}

func randomID() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func hexID(id string) bool {
	if len(id) != 64 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && id == strings.ToLower(id)
}
func validName(name string) bool {
	if name == "" || name != strings.TrimSpace(name) || len(name) > 240 || !utf8.ValidString(name) {
		return false
	}
	for _, c := range name {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}
func parseKey(encoded string) (*ecdsa.PublicKey, string, error) {
	if len(encoded) > 512 {
		return nil, "", ErrInvalid
	}
	der, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.StdEncoding.EncodeToString(der) != encoded {
		return nil, "", ErrInvalid
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, "", ErrInvalid
	}
	key, ok := parsed.(*ecdsa.PublicKey)
	if !ok || key.Curve != elliptic.P256() || !key.Curve.IsOnCurve(key.X, key.Y) {
		return nil, "", ErrInvalid
	}
	canonical, err := x509.MarshalPKIXPublicKey(key)
	if err != nil || !bytes.Equal(canonical, der) {
		return nil, "", ErrInvalid
	}
	sum := sha256.Sum256(der)
	return key, hex.EncodeToString(sum[:]), nil
}

func Open(dir string) (_ *Store, resultErr error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(dir, ".store.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("device store lock unavailable: %w", err)
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, os.Remove(lockPath))
		}
	}()
	_, writeErr := fmt.Fprintf(f, "pid=%d\n", os.Getpid())
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, lockPath: lockPath, devices: make(map[string]record), challenges: make(map[string]challengeState), sessions: make(map[[32]byte]sessionState), rates: make(map[string]rateWindow)}
	f, err = os.Open(filepath.Join(dir, "devices.json"))
	if errors.Is(err, os.ErrNotExist) {
		s.serverID, err = randomID()
		if err != nil {
			return nil, err
		}
		if err := s.save(s.devices); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err := errors.Join(readErr, f.Close()); err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, errors.New("device store too large")
	}
	var snap snapshot
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&snap); err != nil {
		return nil, err
	}
	var extra any
	if d.Decode(&extra) != io.EOF || snap.Version != 1 || !hexID(snap.ServerID) || len(snap.Devices) > maxDevices {
		return nil, errors.New("invalid device snapshot")
	}
	s.serverID = snap.ServerID
	for _, item := range snap.Devices {
		_, id, err := parseKey(item.PublicKey)
		if err != nil || id != item.ID || item.Fingerprint != id || !validName(item.Name) || item.Revision < 1 || (item.Status != "pending" && item.Status != "approved" && item.Status != "revoked") || (item.Admin && item.Status != "approved") {
			return nil, errors.New("invalid device record")
		}
		if _, exists := s.devices[id]; exists {
			return nil, errors.New("duplicate device record")
		}
		s.devices[id] = item
	}
	return s, nil
}
func (s *Store) save(devices map[string]record) error {
	items := make([]record, 0, len(devices))
	for _, item := range devices {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	data, err := json.MarshalIndent(snapshot{Version: 1, ServerID: s.serverID, Devices: items}, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return ErrLimit
	}
	return storage.Replace(filepath.Join(s.dir, "devices.json"), append(data, '\n'))
}
func (s *Store) ServerID() string { return s.serverID }
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	clear(s.challenges)
	clear(s.sessions)
	clear(s.rates)
	return os.Remove(s.lockPath)
}
func (s *Store) Register(publicKey, name string) (Device, error) {
	_, id, err := parseKey(publicKey)
	if err != nil || !validName(name) {
		return Device{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Device{}, ErrClosed
	}
	if item, exists := s.devices[id]; exists {
		return item.Device, nil
	}
	if len(s.devices) >= maxDevices {
		return Device{}, ErrLimit
	}
	item := record{Device: Device{ID: id, Fingerprint: id, Name: name, Status: "pending", Revision: 1}, PublicKey: publicKey}
	updated := make(map[string]record, len(s.devices)+1)
	for k, v := range s.devices {
		updated[k] = v
	}
	updated[id] = item
	if err := s.save(updated); err != nil {
		return Device{}, err
	}
	s.devices = updated
	return item.Device, nil
}
func (s *Store) authorizeAdmin(actorID string, owner bool) error {
	if owner {
		return nil
	}
	actor, err := s.approved(actorID)
	if err != nil || !actor.Admin {
		return ErrAdmin
	}
	return nil
}
func (s *Store) List(actorID string, owner bool) ([]Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	if err := s.authorizeAdmin(actorID, owner); err != nil {
		return nil, err
	}
	items := make([]Device, 0, len(s.devices))
	for _, item := range s.devices {
		items = append(items, item.Device)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}
func (s *Store) Update(id string, revision int, status string, admin *bool, actorID string, owner bool) (Device, error) {
	if !hexID(id) || revision < 1 || (status != "approved" && status != "revoked") {
		return Device{}, ErrInvalid
	}
	if status == "revoked" && admin != nil && *admin {
		return Device{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Device{}, ErrClosed
	}
	if err := s.authorizeAdmin(actorID, owner); err != nil {
		return Device{}, err
	}
	item, exists := s.devices[id]
	if !exists {
		return Device{}, ErrNotFound
	}
	if item.Revision != revision {
		return Device{}, ErrConflict
	}
	if item.Revision == int(^uint(0)>>1) {
		return Device{}, ErrLimit
	}
	item.Status = status
	item.Revision++
	if admin != nil {
		item.Admin = *admin
	}
	if status == "revoked" {
		item.Admin = false
	}
	updated := make(map[string]record, len(s.devices))
	for k, v := range s.devices {
		updated[k] = v
	}
	updated[id] = item
	if err := s.save(updated); err != nil {
		return Device{}, err
	}
	s.devices = updated
	if status == "revoked" {
		for key, session := range s.sessions {
			if session.DeviceID == id {
				delete(s.sessions, key)
			}
		}
		for key, challenge := range s.challenges {
			if challenge.DeviceID == id {
				delete(s.challenges, key)
			}
		}
	}
	return item.Device, nil
}
func (s *Store) approved(id string) (record, error) {
	item, exists := s.devices[id]
	if !exists {
		return record{}, ErrNotFound
	}
	if item.Status == "pending" {
		return record{}, ErrPending
	}
	if item.Status != "approved" {
		return record{}, ErrRevoked
	}
	return item, nil
}
func (s *Store) expire(now time.Time) {
	for id, item := range s.challenges {
		if !now.Before(item.ExpiresAt) {
			delete(s.challenges, id)
		}
	}
	for id, item := range s.sessions {
		if !now.Before(item.ExpiresAt) {
			delete(s.sessions, id)
		}
	}
	for id, window := range s.rates {
		if now.Sub(window.Start) >= time.Minute {
			delete(s.rates, id)
		}
	}
}

// Allow bounds anonymous work both globally and per socket peer, without trusting forwarded headers.
func (s *Store) Allow(operation, peer string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	now := time.Now()
	s.expire(now)
	limits := map[string]int{"register": 6, "challenge": 30, "verify": 60}
	limit, ok := limits[operation]
	if !ok {
		return ErrInvalid
	}
	keys := []string{"global:" + operation, "peer:" + operation + ":" + peer}
	for i, key := range keys {
		window := s.rates[key]
		maximum := limit
		if i == 0 {
			maximum *= 20
		}
		if window.Count >= maximum {
			return ErrRateLimited
		}
		if _, exists := s.rates[key]; !exists && len(s.rates) >= 4096 {
			return ErrRateLimited
		}
	}
	for _, key := range keys {
		window := s.rates[key]
		if window.Count == 0 {
			window.Start = now
		}
		window.Count++
		s.rates[key] = window
	}
	return nil
}
func (s *Store) Challenge(id string) (Challenge, error) {
	if !hexID(id) {
		return Challenge{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Challenge{}, ErrClosed
	}
	if _, err := s.approved(id); err != nil {
		return Challenge{}, err
	}
	now := time.Now().UTC()
	s.expire(now)
	if len(s.challenges) >= 1024 {
		return Challenge{}, ErrLimit
	}
	count := 0
	for _, c := range s.challenges {
		if c.DeviceID == id {
			count++
		}
	}
	if count >= 8 {
		return Challenge{}, ErrLimit
	}
	challengeID, err := randomID()
	if err != nil {
		return Challenge{}, err
	}
	nonce, err := randomID()
	if err != nil {
		return Challenge{}, err
	}
	expires := now.Add(90 * time.Second)
	raw := []byte("chat-agent-device-v1\n" + s.serverID + "\n" + id + "\n" + challengeID + "\n" + nonce + "\n" + expires.Format(time.RFC3339Nano))
	challenge := Challenge{ID: challengeID, Payload: base64.StdEncoding.EncodeToString(raw), ExpiresAt: expires, ServerID: s.serverID}
	s.challenges[challengeID] = challengeState{Challenge: challenge, DeviceID: id, Raw: raw}
	return challenge, nil
}
func (s *Store) Verify(id, challengeID, signature string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Session{}, ErrClosed
	}
	challenge, exists := s.challenges[challengeID]
	delete(s.challenges, challengeID) // Every submitted proof consumes its challenge, even on failure.
	now := time.Now().UTC()
	s.expire(now)
	if !exists || challenge.DeviceID != id || challenge.ServerID != s.serverID || !now.Before(challenge.ExpiresAt) {
		return Session{}, ErrChallenge
	}
	item, err := s.approved(id)
	if err != nil {
		return Session{}, err
	}
	key, _, err := parseKey(item.PublicKey)
	if err != nil {
		return Session{}, err
	}
	sig, err := base64.StdEncoding.Strict().DecodeString(signature)
	if err != nil || len(sig) > 80 || base64.StdEncoding.EncodeToString(sig) != signature {
		return Session{}, ErrSignature
	}
	digest := sha256.Sum256(challenge.Raw)
	if !ecdsa.VerifyASN1(key, digest[:], sig) {
		return Session{}, ErrSignature
	}
	if len(s.sessions) >= 2048 {
		return Session{}, ErrLimit
	}
	count := 0
	for _, session := range s.sessions {
		if session.DeviceID == id {
			count++
		}
	}
	if count >= 16 {
		return Session{}, ErrLimit
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return Session{}, err
	}
	token := "dvc1_" + base64.RawURLEncoding.EncodeToString(secret[:])
	expires := now.Add(20 * time.Minute)
	s.sessions[sha256.Sum256([]byte(token))] = sessionState{DeviceID: id, ExpiresAt: expires}
	return Session{Token: token, ExpiresAt: expires, Device: item.Device, ServerID: s.serverID}, nil
}
func (s *Store) Authenticate(token string) (Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Device{}, ErrClosed
	}
	key := sha256.Sum256([]byte(token))
	session, exists := s.sessions[key]
	if !exists || !time.Now().Before(session.ExpiresAt) {
		delete(s.sessions, key)
		return Device{}, ErrSession
	}
	item, err := s.approved(session.DeviceID)
	if err != nil {
		delete(s.sessions, key)
		return Device{}, err
	}
	return item.Device, nil
}
func (s *Store) Logout(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sha256.Sum256([]byte(token)))
}
