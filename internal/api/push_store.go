package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"pocket-agent/internal/storage"
)

// Only device opt-ins are persisted. Inbox content remains in its original
// role store; live receiver leases expire after 90 seconds and never survive a
// server restart. A stale subscription alone cannot generate paid care.
type pushStore struct {
	mu      sync.Mutex
	dir     string
	enabled map[string]bool
	leases  map[string]time.Time
	closed  bool
}

func openPush(dir string) (_ *pushStore, err error) {
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".store.lock"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		_ = os.Remove(filepath.Join(dir, ".store.lock"))
		return nil, err
	}
	s := &pushStore{dir: dir, enabled: map[string]bool{}, leases: map[string]time.Time{}}
	defer func() {
		if err != nil {
			_ = s.close()
		}
	}()
	file, err := os.Open(filepath.Join(dir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err = errors.Join(readErr, file.Close()); err != nil {
		return nil, err
	}
	if len(data) > 64<<10 {
		return nil, errors.New("push settings too large")
	}
	var state struct {
		Version int             `json:"version"`
		Devices map[string]bool `json:"devices"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var extra any
	if d.Decode(&state) != nil || d.Decode(&extra) != io.EOF || state.Version != 1 || len(state.Devices) > 128 {
		return nil, errors.New("invalid push settings")
	}
	for id, enabled := range state.Devices {
		if !storage.ValidID(id) {
			return nil, errors.New("invalid push device")
		}
		if enabled {
			s.enabled[id] = true
		}
	}
	return s, nil
}

func (s *pushStore) set(id string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !storage.ValidID(id) {
		return errors.New("push settings unavailable")
	}
	if s.enabled[id] == enabled {
		return nil
	}
	next := make(map[string]bool, len(s.enabled)+1)
	for key, value := range s.enabled {
		next[key] = value
	}
	if enabled {
		next[id] = true
	} else {
		delete(next, id)
	}
	if len(next) > 128 {
		return errors.New("push device limit reached")
	}
	data, err := json.Marshal(struct {
		Version int             `json:"version"`
		Devices map[string]bool `json:"devices"`
	}{1, next})
	if err != nil {
		return err
	}
	if err = storage.Replace(filepath.Join(s.dir, "state.json"), data); err != nil {
		return err
	}
	s.enabled = next
	if !enabled {
		delete(s.leases, id)
	}
	return nil
}

func (s *pushStore) get(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && s.enabled[id]
}

func (s *pushStore) touch(id string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !s.enabled[id] {
		return false
	}
	s.leases[id] = now.Add(90 * time.Second)
	return true
}

func (s *pushStore) live(approved map[string]bool, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	for id, expiry := range s.leases {
		if !expiry.After(now) || !approved[id] {
			delete(s.leases, id)
			continue
		}
		if s.enabled[id] {
			return true
		}
	}
	return false
}

func (s *pushStore) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return os.Remove(filepath.Join(s.dir, ".store.lock"))
}
