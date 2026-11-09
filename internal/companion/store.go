// Package companion owns role-scoped interaction preferences and context policy.
package companion

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"chat-agent/internal/storage"
)

var ErrConflict = errors.New("companion preferences changed; reload before saving")
var ErrInvalid = errors.New("invalid companion preferences")

type Preferences struct {
	PreferredName string `json:"preferred_name"`
	ReplyLength   string `json:"reply_length"`
	Tone          string `json:"tone"`
	Advice        string `json:"advice"`
	Questions     string `json:"questions"`
}
type Snapshot struct {
	Version     int         `json:"version"`
	RoleID      string      `json:"role_id"`
	Revision    int         `json:"revision"`
	Preferences Preferences `json:"preferences"`
	UpdatedAt   *time.Time  `json:"updated_at,omitempty"`
}
type Patch struct {
	Revision      int     `json:"revision"`
	PreferredName *string `json:"preferred_name,omitempty"`
	ReplyLength   *string `json:"reply_length,omitempty"`
	Tone          *string `json:"tone,omitempty"`
	Advice        *string `json:"advice,omitempty"`
	Questions     *string `json:"questions,omitempty"`
}
type Store struct {
	mu        sync.Mutex
	dir, lock string
	closed    bool
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock := filepath.Join(dir, ".store.lock")
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("companion store lock unavailable: %w", err)
	}
	_, err = fmt.Fprintf(f, "pid=%d\n", os.Getpid())
	err = errors.Join(err, f.Close())
	if err != nil {
		os.Remove(lock)
		return nil, err
	}
	return &Store{dir: dir, lock: lock}, nil
}
func defaults(role string) Snapshot {
	return Snapshot{Version: 1, RoleID: role, Preferences: Preferences{ReplyLength: "default", Tone: "default", Advice: "default", Questions: "default"}}
}
func valid(p Preferences) bool {
	name := p.PreferredName
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > 40 || strings.TrimSpace(name) != name || strings.ContainsAny(name, "<>\u2028\u2029") {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	in := func(s string, values ...string) bool {
		for _, v := range values {
			if s == v {
				return true
			}
		}
		return false
	}
	return in(p.ReplyLength, "default", "short", "detailed") && in(p.Tone, "default", "natural", "warm", "playful") && in(p.Advice, "default", "ask_first", "on_request") && in(p.Questions, "default", "light", "normal")
}
func (s *Store) load(role string) (Snapshot, error) {
	if s.closed {
		return Snapshot{}, errors.New("companion store closed")
	}
	if !storage.ValidID(role) {
		return Snapshot{}, ErrInvalid
	}
	f, err := os.Open(filepath.Join(s.dir, role+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return defaults(role), nil
	}
	if err != nil {
		return Snapshot{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 8193))
	if err != nil {
		return Snapshot{}, err
	}
	if len(b) > 8192 {
		return Snapshot{}, errors.New("companion file too large")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	var snap Snapshot
	var extra any
	if err = d.Decode(&snap); err != nil {
		return Snapshot{}, err
	}
	if d.Decode(&extra) != io.EOF || snap.Version != 1 || snap.RoleID != role || snap.Revision < 1 || !valid(snap.Preferences) {
		return Snapshot{}, errors.New("damaged companion preferences")
	}
	return snap, nil
}
func (s *Store) Get(role string) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load(role)
}
func (s *Store) Update(role string, patch Patch) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap, err := s.load(role)
	if err != nil {
		return Snapshot{}, err
	}
	if patch.Revision != snap.Revision {
		return Snapshot{}, ErrConflict
	}
	if patch.PreferredName == nil && patch.ReplyLength == nil && patch.Tone == nil && patch.Advice == nil && patch.Questions == nil {
		return Snapshot{}, ErrInvalid
	}
	p := snap.Preferences
	if patch.PreferredName != nil {
		p.PreferredName = strings.TrimSpace(*patch.PreferredName)
	}
	if patch.ReplyLength != nil {
		p.ReplyLength = *patch.ReplyLength
	}
	if patch.Tone != nil {
		p.Tone = *patch.Tone
	}
	if patch.Advice != nil {
		p.Advice = *patch.Advice
	}
	if patch.Questions != nil {
		p.Questions = *patch.Questions
	}
	if !valid(p) {
		return Snapshot{}, ErrInvalid
	}
	if p == snap.Preferences {
		return snap, nil
	}
	now := time.Now().UTC()
	snap.Preferences = p
	snap.Revision++
	snap.UpdatedAt = &now
	b, err := json.Marshal(snap)
	if err != nil {
		return Snapshot{}, err
	}
	if err = storage.Replace(filepath.Join(s.dir, role+".json"), b); err != nil {
		return Snapshot{}, err
	}
	return snap, nil
}
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return os.Remove(s.lock)
}
