package channel

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"chat-agent/internal/storage"
)

const identityStoreVersion = 1

type ChannelIdentity struct {
	ID           string    `json:"id"`
	PrincipalID  string    `json:"principal_id"`
	Platform     string    `json:"platform"`
	AccountID    string    `json:"account_id"`
	RemoteUserID string    `json:"remote_user_id"`
	VerifiedAt   time.Time `json:"verified_at"`
}

type LinkCode struct {
	Hash        string    `json:"hash"`
	PrincipalID string    `json:"principal_id"`
	ExpiresAt   time.Time `json:"expires_at"`
	CreatedAt   time.Time `json:"created_at"`
}

type identityState struct {
	Version    int                        `json:"version"`
	Identities map[string]ChannelIdentity `json:"identities"`
	Codes      map[string]LinkCode        `json:"codes"`
}

type IdentityStore struct {
	mu     sync.Mutex
	dir    string
	lock   string
	state  identityState
	closed bool
}

func OpenIdentityStore(dir string) (_ *IdentityStore, err error) {
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock := filepath.Join(dir, ".identity.lock")
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		_ = os.Remove(lock)
		return nil, err
	}
	s := &IdentityStore{dir: dir, lock: lock, state: identityState{Version: identityStoreVersion, Identities: map[string]ChannelIdentity{}, Codes: map[string]LinkCode{}}}
	defer func() {
		if err != nil {
			_ = s.Close()
		}
	}()
	raw, readErr := os.ReadFile(filepath.Join(dir, "identities.json"))
	if errors.Is(readErr, os.ErrNotExist) {
		return s, nil
	}
	if readErr != nil || len(raw) > 1<<20 || json.Unmarshal(raw, &s.state) != nil || !validIdentityState(s.state) {
		return nil, errors.New("invalid channel identity store")
	}
	if s.pruneLocked(time.Now().UTC()) {
		err = s.saveLocked()
	}
	return s, err
}

func identityID(platform, accountID, remoteUserID string) (string, error) {
	parts := []string{strings.TrimSpace(platform), strings.TrimSpace(accountID), strings.TrimSpace(remoteUserID)}
	for _, part := range parts {
		if part == "" || len(part) > 512 {
			return "", ErrInvalidEnvelope
		}
	}
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(digest[:]), nil
}

func validIdentityState(state identityState) bool {
	if state.Version != identityStoreVersion || state.Identities == nil || state.Codes == nil || len(state.Identities) > 256 || len(state.Codes) > 64 {
		return false
	}
	for id, value := range state.Identities {
		want, err := identityID(value.Platform, value.AccountID, value.RemoteUserID)
		if err != nil || id != want || value.ID != id || !storage.ValidID(value.PrincipalID) || value.VerifiedAt.IsZero() {
			return false
		}
	}
	for hash, value := range state.Codes {
		if hash != value.Hash || len(hash) != 64 || !storage.ValidID(value.PrincipalID) || value.ExpiresAt.IsZero() || value.CreatedAt.IsZero() || value.ExpiresAt.Before(value.CreatedAt) {
			return false
		}
	}
	return true
}

func (s *IdentityStore) saveLocked() error {
	if s.closed {
		return ErrClosed
	}
	raw, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	return storage.Replace(filepath.Join(s.dir, "identities.json"), raw)
}

func (s *IdentityStore) pruneLocked(now time.Time) bool {
	changed := false
	for hash, code := range s.state.Codes {
		if !code.ExpiresAt.After(now) {
			delete(s.state.Codes, hash)
			changed = true
		}
	}
	return changed
}

func (s *IdentityStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return os.Remove(s.lock)
}

func (s *IdentityStore) Bind(principalID, platform, accountID, remoteUserID string) (ChannelIdentity, error) {
	if !storage.ValidID(principalID) {
		return ChannelIdentity{}, ErrInvalidEnvelope
	}
	id, err := identityID(platform, accountID, remoteUserID)
	if err != nil {
		return ChannelIdentity{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ChannelIdentity{}, ErrClosed
	}
	if existing, ok := s.state.Identities[id]; ok {
		if existing.PrincipalID != principalID {
			return ChannelIdentity{}, ErrConflict
		}
		return existing, nil
	}
	if len(s.state.Identities) >= 256 {
		return ChannelIdentity{}, ErrLimit
	}
	value := ChannelIdentity{ID: id, PrincipalID: principalID, Platform: strings.TrimSpace(platform), AccountID: strings.TrimSpace(accountID), RemoteUserID: strings.TrimSpace(remoteUserID), VerifiedAt: time.Now().UTC()}
	s.state.Identities[id] = value
	if err := s.saveLocked(); err != nil {
		delete(s.state.Identities, id)
		return ChannelIdentity{}, err
	}
	return value, nil
}

func (s *IdentityStore) Resolve(platform, accountID, remoteUserID string) (ChannelIdentity, bool) {
	id, err := identityID(platform, accountID, remoteUserID)
	if err != nil {
		return ChannelIdentity{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.state.Identities[id]
	return value, ok && !s.closed
}

func (s *IdentityStore) Issue(principalID string, ttl time.Duration) (string, LinkCode, error) {
	if !storage.ValidID(principalID) || ttl < time.Minute || ttl > time.Hour {
		return "", LinkCode{}, ErrInvalidEnvelope
	}
	var secret [15]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", LinkCode{}, err
	}
	plain := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret[:])
	digest := sha256.Sum256([]byte(plain))
	hash := hex.EncodeToString(digest[:])
	now := time.Now().UTC()
	code := LinkCode{Hash: hash, PrincipalID: principalID, CreatedAt: now, ExpiresAt: now.Add(ttl)}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", LinkCode{}, ErrClosed
	}
	s.pruneLocked(now)
	if len(s.state.Codes) >= 64 {
		return "", LinkCode{}, ErrLimit
	}
	s.state.Codes[hash] = code
	if err := s.saveLocked(); err != nil {
		delete(s.state.Codes, hash)
		return "", LinkCode{}, err
	}
	return plain, code, nil
}

func (s *IdentityStore) Consume(plain, platform, accountID, remoteUserID string) (ChannelIdentity, error) {
	plain = strings.ToUpper(strings.TrimSpace(plain))
	digest := sha256.Sum256([]byte(plain))
	hash := hex.EncodeToString(digest[:])
	id, err := identityID(platform, accountID, remoteUserID)
	if err != nil {
		return ChannelIdentity{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ChannelIdentity{}, ErrClosed
	}
	now := time.Now().UTC()
	s.pruneLocked(now)
	code, ok := s.state.Codes[hash]
	if !ok || !code.ExpiresAt.After(now) {
		return ChannelIdentity{}, ErrNotFound
	}
	if existing, exists := s.state.Identities[id]; exists && existing.PrincipalID != code.PrincipalID {
		return ChannelIdentity{}, ErrConflict
	}
	value := ChannelIdentity{ID: id, PrincipalID: code.PrincipalID, Platform: strings.TrimSpace(platform), AccountID: strings.TrimSpace(accountID), RemoteUserID: strings.TrimSpace(remoteUserID), VerifiedAt: now}
	previous, existed := s.state.Identities[id]
	s.state.Identities[id] = value
	delete(s.state.Codes, hash)
	if err := s.saveLocked(); err != nil {
		if existed {
			s.state.Identities[id] = previous
		} else {
			delete(s.state.Identities, id)
		}
		s.state.Codes[hash] = code
		return ChannelIdentity{}, err
	}
	return value, nil
}

func (s *IdentityStore) List() []ChannelIdentity {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]ChannelIdentity, 0, len(s.state.Identities))
	for _, value := range s.state.Identities {
		items = append(items, value)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].VerifiedAt.After(items[j].VerifiedAt) })
	return items
}
