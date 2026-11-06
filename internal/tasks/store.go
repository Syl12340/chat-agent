// Package tasks stores role-scoped todos and one-shot reminders. Reminders
// transition durably into an inbox; they never execute code or call a model.
package tasks

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
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

const (
	maxSnapshotBytes   = 16 << 20
	maxReceipts        = 32768
	maxReceiptsPerRole = 8192
)

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

// Receipt is the durable proof of one protected business mutation. It stores
// no prompt or evidence text and is committed in the same state replacement as
// the task change it describes.
type Receipt struct {
	RoleID             string    `json:"role_id"`
	OperationID        string    `json:"operation_id"`
	ArgumentsSHA256    string    `json:"arguments_sha256"`
	OperationKind      string    `json:"operation_kind"`
	CommittedAt        time.Time `json:"committed_at"`
	EntityIDs          []string  `json:"entity_ids"`
	ResultingRevisions []int     `json:"resulting_revisions"`
	OutcomeDigest      string    `json:"outcome_digest"`
}
type snapshot struct {
	Version  int       `json:"version"`
	Items    []Item    `json:"items"`
	Receipts []Receipt `json:"receipts,omitempty"`
}
type Store struct {
	mu        sync.Mutex
	dir, lock string
	items     []Item
	receipts  []Receipt
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
	data, err := io.ReadAll(io.LimitReader(f, maxSnapshotBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSnapshotBytes {
		return nil, errors.New("task snapshot too large")
	}
	var snap snapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&snap); err != nil {
		return nil, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || (snap.Version != 1 && snap.Version != 2) || len(snap.Items) > 2000 || len(snap.Receipts) > maxReceipts || snap.Version == 1 && len(snap.Receipts) != 0 {
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
	receiptKeys := map[string]bool{}
	receiptRoles := map[string]int{}
	itemsByID := map[string]Item{}
	for _, item := range snap.Items {
		itemsByID[item.ID] = item
	}
	for _, receipt := range snap.Receipts {
		key := receipt.RoleID + "\x00" + receipt.OperationID
		receiptRoles[receipt.RoleID]++
		valid := validReceipt(receipt) && !receiptKeys[key] && receiptRoles[receipt.RoleID] <= maxReceiptsPerRole
		if valid {
			for i, id := range receipt.EntityIDs {
				if item, exists := itemsByID[id]; exists && (item.RoleID != receipt.RoleID || item.Revision < receipt.ResultingRevisions[i]) {
					valid = false
				}
			}
		}
		if !valid {
			return nil, errors.New("invalid task receipt")
		}
		receiptKeys[key] = true
	}
	s.items = snap.Items
	s.receipts = snap.Receipts
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
func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}
func validReceipt(receipt Receipt) bool {
	if !storage.ValidID(receipt.RoleID) || !storage.ValidID(receipt.OperationID) || !storage.ValidID(receipt.OperationKind) || !validDigest(receipt.ArgumentsSHA256) || !validDigest(receipt.OutcomeDigest) || receipt.CommittedAt.IsZero() || len(receipt.EntityIDs) == 0 || len(receipt.EntityIDs) > 8 || len(receipt.EntityIDs) != len(receipt.ResultingRevisions) {
		return false
	}
	for i, id := range receipt.EntityIDs {
		if !storage.ValidID(id) || receipt.ResultingRevisions[i] < 1 {
			return false
		}
	}
	return true
}
func copyReceipt(receipt Receipt) Receipt {
	receipt.EntityIDs = append([]string(nil), receipt.EntityIDs...)
	receipt.ResultingRevisions = append([]int(nil), receipt.ResultingRevisions...)
	return receipt
}
func (s *Store) save(items []Item, receipts []Receipt) error {
	data, err := json.MarshalIndent(snapshot{Version: 2, Items: items, Receipts: receipts}, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > maxSnapshotBytes {
		return errors.New("task snapshot too large")
	}
	if err := storage.Replace(filepath.Join(s.dir, "state.json"), append(data, '\n')); err != nil {
		return err
	}
	s.items = items
	s.receipts = receipts
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
	item, _, _, err := s.add(role, kind, title, key, source, session, due, now, "", "")
	return item, err
}

// AddWithReceipt protects task creation with an operation receipt committed in
// the same state.json replacement. A replay of the same operation and argument
// digest returns its original receipt without creating another task.
func (s *Store) AddWithReceipt(role, kind, title, operationID, argumentsSHA256, operationKind, source, session string, due *time.Time, now time.Time) (Item, Receipt, bool, error) {
	return s.add(role, kind, title, operationID, source, session, due, now, argumentsSHA256, operationKind)
}

func (s *Store) add(role, kind, title, key, source, session string, due *time.Time, now time.Time, argumentsSHA256, operationKind string) (Item, Receipt, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Item{}, Receipt{}, false, errors.New("task store closed")
	}
	title = strings.Join(strings.Fields(title), " ")
	now = now.UTC()
	if !storage.ValidID(role) || !validTitle(title) || !validKey(key) || (kind != "todo" && kind != "reminder") {
		return Item{}, Receipt{}, false, errors.New("invalid task title, kind or key")
	}
	protected := argumentsSHA256 != "" || operationKind != ""
	if protected && (!validDigest(argumentsSHA256) || !storage.ValidID(operationKind)) {
		return Item{}, Receipt{}, false, errors.New("invalid task receipt identity")
	}
	if protected {
		for _, receipt := range s.receipts {
			if receipt.RoleID != role || receipt.OperationID != key {
				continue
			}
			if receipt.ArgumentsSHA256 != argumentsSHA256 || receipt.OperationKind != operationKind {
				return Item{}, Receipt{}, true, ErrConflict
			}
			for _, item := range s.items {
				if item.RoleID == role && len(receipt.EntityIDs) == 1 && item.ID == receipt.EntityIDs[0] {
					return copyItem(item), copyReceipt(receipt), true, nil
				}
			}
			return Item{}, copyReceipt(receipt), true, nil
		}
		roleReceipts := 0
		for _, receipt := range s.receipts {
			if receipt.RoleID == role {
				roleReceipts++
			}
		}
		if len(s.receipts) >= maxReceipts || roleReceipts >= maxReceiptsPerRole {
			return Item{}, Receipt{}, false, errors.New("task receipt limit reached")
		}
	}
	var at *time.Time
	if due != nil {
		value := due.UTC()
		at = &value
	}
	if kind == "todo" && at != nil || kind == "reminder" && at == nil {
		return Item{}, Receipt{}, false, errors.New("invalid task due time")
	}
	// Resolve key ownership before semantic deduplication: an existing key for
	// another item must never be rebound to an earlier matching title.
	for _, item := range s.items {
		if item.RoleID == role && (item.Key == key || slices.Contains(item.RetryKeys, key)) {
			same := item.Kind == kind && strings.EqualFold(item.Title, title) && (at == nil && item.DueAt == nil || at != nil && item.DueAt != nil && at.Equal(*item.DueAt))
			if !same {
				return Item{}, Receipt{}, false, ErrConflict
			}
			if !protected {
				return copyItem(item), Receipt{}, false, nil
			}
			receipt := newReceipt(role, key, argumentsSHA256, operationKind, item, now)
			receipts := append(append([]Receipt(nil), s.receipts...), receipt)
			if err := s.save(append([]Item(nil), s.items...), receipts); err != nil {
				return Item{}, Receipt{}, false, err
			}
			return copyItem(item), copyReceipt(receipt), false, nil
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
				return Item{}, Receipt{}, false, ErrConflict
			}
			item.RetryKeys = append(append([]string(nil), item.RetryKeys...), key)
			items := append([]Item(nil), s.items...)
			items[i] = item
			receipts := append([]Receipt(nil), s.receipts...)
			var receipt Receipt
			if protected {
				receipt = newReceipt(role, key, argumentsSHA256, operationKind, item, now)
				receipts = append(receipts, receipt)
			}
			if err := s.save(items, receipts); err != nil {
				return Item{}, Receipt{}, false, err
			}
			return copyItem(item), copyReceipt(receipt), false, nil
		}
	}
	if at != nil && (at.Before(now.Add(-time.Minute)) || at.After(now.Add(366*24*time.Hour))) {
		return Item{}, Receipt{}, false, errors.New("reminder time must be within one minute past and one year ahead")
	}
	if count >= 500 || len(s.items) >= 2000 {
		return Item{}, Receipt{}, false, errors.New("task limit reached")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Item{}, Receipt{}, false, err
	}
	status := "open"
	if kind == "reminder" {
		status = "scheduled"
	}
	item := Item{ID: hex.EncodeToString(id[:]), RoleID: role, Kind: kind, Title: title, Status: status, DueAt: at, Key: key, Source: source, SessionID: session, Revision: 1, CreatedAt: now, UpdatedAt: now}
	items := append(append([]Item(nil), s.items...), item)
	receipts := append([]Receipt(nil), s.receipts...)
	var receipt Receipt
	if protected {
		receipt = newReceipt(role, key, argumentsSHA256, operationKind, item, now)
		receipts = append(receipts, receipt)
	}
	if err := s.save(items, receipts); err != nil {
		return Item{}, Receipt{}, false, err
	}
	return copyItem(item), copyReceipt(receipt), false, nil
}

func newReceipt(role, operationID, argumentsSHA256, operationKind string, item Item, now time.Time) Receipt {
	outcome, _ := json.Marshal(struct {
		ID       string `json:"id"`
		Revision int    `json:"revision"`
	}{item.ID, item.Revision})
	digest := sha256.Sum256(outcome)
	return Receipt{RoleID: role, OperationID: operationID, ArgumentsSHA256: argumentsSHA256, OperationKind: operationKind, CommittedAt: now.UTC(), EntityIDs: []string{item.ID}, ResultingRevisions: []int{item.Revision}, OutcomeDigest: hex.EncodeToString(digest[:])}
}

func (s *Store) Receipt(role, operationID string) (Receipt, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Receipt{}, false, errors.New("task store closed")
	}
	if !storage.ValidID(role) || !validKey(operationID) {
		return Receipt{}, false, errors.New("invalid task receipt identity")
	}
	for _, receipt := range s.receipts {
		if receipt.RoleID == role && receipt.OperationID == operationID {
			return copyReceipt(receipt), true, nil
		}
	}
	return Receipt{}, false, nil
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
		if err := s.save(items, s.receipts); err != nil {
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
			return s.save(items, s.receipts)
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
	if err := s.save(items, s.receipts); err != nil {
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
