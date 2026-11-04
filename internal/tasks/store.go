// Package tasks stores role-scoped todos and one-shot reminders. Reminders
// transition durably into an inbox; they never execute code or call a model.
package tasks

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"

	"pocket-agent/internal/storage"
)

var ErrNotFound = errors.New("task not found")
var ErrConflict = errors.New("task changed or idempotency key reused")

type Item struct {
	ID        string     `json:"id"`
	RoleID    string     `json:"role_id"`
	Kind      string     `json:"kind"`
	Title     string     `json:"title"`
	Status    string     `json:"status"`
	DueAt     *time.Time `json:"due_at,omitempty"`
	FiredAt   *time.Time `json:"fired_at,omitempty"`
	Revision  int        `json:"revision"`
	Source    string     `json:"source"`
	SessionID string     `json:"session_id,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	Key       string     `json:"idempotency_key"`
	RetryKeys []string   `json:"retry_keys,omitempty"`
}
type snapshot struct {
	Version int    `json:"version"`
	Items   []Item `json:"items"`
}
type Store struct {
	mu        sync.Mutex
	dir, lock string
	items     []Item
	closed    bool
}

func Open(dir string) (s *Store, err error) {
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(dir, ".store.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("task store lock unavailable: %w", err)
	}
	_, writeErr := fmt.Fprintf(f, "pid=%d\n", os.Getpid())
	if err = errors.Join(writeErr, f.Close()); err != nil {
		os.Remove(lockPath)
		return nil, err
	}
	s = &Store{dir: dir, lock: lockPath, items: []Item{}}
	owned := s
	defer func() {
		if err != nil {
			owned.Close()
			s = nil
		}
	}()
	f, err = os.Open(filepath.Join(dir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 4<<20 {
		return nil, errors.New("task snapshot too large")
	}
	var snap snapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&snap); err != nil {
		return nil, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || snap.Version != 1 || len(snap.Items) > 2000 {
		return nil, errors.New("invalid task snapshot")
	}
	seen := map[string]bool{}
	keys := map[string]bool{}
	for _, item := range snap.Items {
		if !storage.ValidID(item.ID) || !storage.ValidID(item.RoleID) || seen[item.ID] || !validTitle(item.Title) || !validStatus(item.Kind, item.Status) || item.Revision < 1 || !validKey(item.Key) {
			return nil, errors.New("invalid task record")
		}
		if item.Kind == "reminder" && (item.DueAt == nil || item.DueAt.IsZero() || (item.Status == "due" || item.Status == "acknowledged") && item.FiredAt == nil) {
			return nil, errors.New("invalid reminder record")
		}
		seen[item.ID] = true
		if item.Kind == "todo" && (item.DueAt != nil || item.FiredAt != nil) || item.Status == "scheduled" && item.FiredAt != nil || len(item.RetryKeys) > 16 {
			return nil, errors.New("invalid task state")
		}
		for _, key := range append([]string{item.Key}, item.RetryKeys...) {
			index := item.RoleID + "\x00" + key
			if !validKey(key) || keys[index] {
				return nil, errors.New("invalid task idempotency key")
			}
			keys[index] = true
		}
	}
	s.items = snap.Items
	return s, nil
}
func validTitle(title string) bool { return strings.TrimSpace(title) != "" && len(title) <= 256 }
func validKey(key string) bool     { return len(key) >= 8 && len(key) <= 128 && storage.ValidID(key) }
func validStatus(kind, status string) bool {
	if kind == "todo" {
		return status == "open" || status == "done"
	}
	return kind == "reminder" && (status == "scheduled" || status == "due" || status == "acknowledged" || status == "canceled")
}
func copyItem(item Item) Item {
	item.RetryKeys = append([]string(nil), item.RetryKeys...)
	if item.DueAt != nil {
		v := *item.DueAt
		item.DueAt = &v
	}
	if item.FiredAt != nil {
		v := *item.FiredAt
		item.FiredAt = &v
	}
	return item
}
func (s *Store) save(items []Item) error {
	data, err := json.MarshalIndent(snapshot{Version: 1, Items: items}, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > 4<<20 {
		return errors.New("task snapshot too large")
	}
	if err := storage.Replace(filepath.Join(s.dir, "state.json"), append(data, '\n')); err != nil {
		return err
	}
	s.items = items
	return nil
}
func (s *Store) List(role, kind string) ([]Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("task store closed")
	}
	if !storage.ValidID(role) {
		return nil, errors.New("invalid role")
	}
	out := []Item{}
	for _, item := range s.items {
		if item.RoleID == role && (kind == "" || item.Kind == kind) {
			out = append(out, copyItem(item))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}
func (s *Store) Add(role, kind, title, key, source, session string, due *time.Time, now time.Time) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Item{}, errors.New("task store closed")
	}
	title = strings.Join(strings.Fields(title), " ")
	now = now.UTC()
	if !storage.ValidID(role) || !validTitle(title) || !validKey(key) || (kind != "todo" && kind != "reminder") {
		return Item{}, errors.New("invalid task title, kind or key")
	}
	var at *time.Time
	if due != nil {
		value := due.UTC()
		at = &value
	}
	if kind == "todo" && at != nil || kind == "reminder" && at == nil {
		return Item{}, errors.New("invalid task due time")
	}
	// Resolve key ownership before semantic deduplication: an existing key for
	// another item must never be rebound to an earlier matching title.
	for _, item := range s.items {
		if item.RoleID == role && (item.Key == key || slices.Contains(item.RetryKeys, key)) {
			same := item.Kind == kind && strings.EqualFold(item.Title, title) && (at == nil && item.DueAt == nil || at != nil && item.DueAt != nil && at.Equal(*item.DueAt))
			if !same {
				return Item{}, ErrConflict
			}
			return copyItem(item), nil
		}
	}
	count := 0
	for i, item := range s.items {
		if item.RoleID != role {
			continue
		}
		count++
		same := item.Kind == kind && strings.EqualFold(item.Title, title) && (at == nil && item.DueAt == nil || at != nil && item.DueAt != nil && at.Equal(*item.DueAt))
		if same && (item.Status == "open" || item.Status == "scheduled" || item.Status == "due") {
			if len(item.RetryKeys) >= 16 {
				return Item{}, ErrConflict
			}
			item.RetryKeys = append(append([]string(nil), item.RetryKeys...), key)
			items := append([]Item(nil), s.items...)
			items[i] = item
			if err := s.save(items); err != nil {
				return Item{}, err
			}
			return copyItem(item), nil
		}
	}
	if at != nil && (at.Before(now.Add(-time.Minute)) || at.After(now.Add(366*24*time.Hour))) {
		return Item{}, errors.New("reminder time must be within one minute past and one year ahead")
	}
	if count >= 500 || len(s.items) >= 2000 {
		return Item{}, errors.New("task limit reached")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Item{}, err
	}
	status := "open"
	if kind == "reminder" {
		status = "scheduled"
	}
	item := Item{ID: hex.EncodeToString(id[:]), RoleID: role, Kind: kind, Title: title, Status: status, DueAt: at, Key: key, Source: source, SessionID: session, Revision: 1, CreatedAt: now, UpdatedAt: now}
	items := append(append([]Item(nil), s.items...), item)
	if err := s.save(items); err != nil {
		return Item{}, err
	}
	return copyItem(item), nil
}
func (s *Store) Update(role, id, status string, revision int, now time.Time) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Item{}, errors.New("task store closed")
	}
	items := append([]Item(nil), s.items...)
	for i, item := range items {
		if item.ID != id || item.RoleID != role {
			continue
		}
		if item.Status == status {
			return copyItem(item), nil
		}
		if item.Revision != revision {
			return Item{}, ErrConflict
		}
		allowed := item.Kind == "todo" && (status == "open" || status == "done") || item.Kind == "reminder" && ((item.Status == "scheduled" || item.Status == "due") && status == "canceled" || item.Status == "due" && status == "acknowledged")
		if !allowed {
			return Item{}, errors.New("invalid task transition")
		}
		item.Status = status
		item.Revision++
		item.UpdatedAt = now.UTC()
		items[i] = item
		if err := s.save(items); err != nil {
			return Item{}, err
		}
		return copyItem(item), nil
	}
	return Item{}, ErrNotFound
}
func (s *Store) Delete(role, id string, revision int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("task store closed")
	}
	for i, item := range s.items {
		if item.RoleID == role && item.ID == id {
			if item.Revision != revision {
				return ErrConflict
			}
			items := append([]Item(nil), s.items[:i]...)
			items = append(items, s.items[i+1:]...)
			return s.save(items)
		}
	}
	return ErrNotFound
}

// Advance commits each scheduled -> due transition once, including overdue
// reminders after restart. Failed writes retain the previous state for retry.
func (s *Store) Advance(now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, errors.New("task store closed")
	}
	items := append([]Item(nil), s.items...)
	count := 0
	now = now.UTC()
	for i, item := range items {
		if item.Kind == "reminder" && item.Status == "scheduled" && !item.DueAt.After(now) {
			item.Status = "due"
			at := now
			item.FiredAt = &at
			item.UpdatedAt = now
			item.Revision++
			items[i] = item
			count++
		}
	}
	if count == 0 {
		return 0, nil
	}
	if err := s.save(items); err != nil {
		return 0, err
	}
	return count, nil
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
