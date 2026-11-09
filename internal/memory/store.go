// Package memory stores reviewable facts separately from conversation history.
package memory

import (
	"bytes"
	"context"
	"crypto/rand"
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

	"chat-agent/internal/retrieval"
	"chat-agent/internal/storage"
)

var ErrNotFound = errors.New("memory not found")
var ErrConflict = errors.New("memory changed; refresh before editing")

type Entry struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	Status    string    `json:"status"`
	Source    string    `json:"source"`
	SessionID string    `json:"session_id,omitempty"`
	Revision  int       `json:"revision"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Reference struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
}
type snapshot struct {
	Version int     `json:"version"`
	RoleID  string  `json:"role_id"`
	Entries []Entry `json:"entries"`
}

// Reranker reorders candidate documents by semantic relevance. Implementations
// must be safe to call concurrently and must fail soft: the store keeps its
// keyword ordering whenever the second result is false.
type Reranker interface {
	Rerank(query string, documents []string, limit int) ([]int, bool)
}

type ContextReranker interface {
	RerankContext(context.Context, string, []string, int) ([]int, bool)
}

type Store struct {
	mu       sync.Mutex
	dir      string
	lockPath string
	cache    map[string][]Entry
	closed   bool
	reranker Reranker
}

// SetReranker installs the optional semantic layer. Passing nil restores pure
// keyword ranking.
func (s *Store) SetReranker(r Reranker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reranker = r
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(dir, ".store.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("memory store lock unavailable: %w", err)
	}
	_, writeErr := fmt.Fprintf(f, "pid=%d\n", os.Getpid())
	if err := errors.Join(writeErr, f.Close()); err != nil {
		os.Remove(lockPath)
		return nil, err
	}
	return &Store{dir: dir, lockPath: lockPath, cache: map[string][]Entry{}}, nil
}
func valid(content, status string) bool {
	return strings.TrimSpace(content) != "" && len(content) <= 4096 && (status == "pending" || status == "approved")
}
func (s *Store) load(role string) ([]Entry, error) {
	if s.closed {
		return nil, errors.New("memory store closed")
	}
	if !storage.ValidID(role) {
		return nil, errors.New("invalid role")
	}
	if entries, ok := s.cache[role]; ok {
		return append([]Entry(nil), entries...), nil
	}
	f, err := os.Open(filepath.Join(s.dir, role+".json"))
	if errors.Is(err, os.ErrNotExist) {
		s.cache[role] = []Entry{}
		return []Entry{}, nil
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
		return nil, errors.New("memory file too large")
	}
	var snap snapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snap); err != nil {
		return nil, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || snap.Version != 1 || snap.RoleID != role || len(snap.Entries) > 500 {
		return nil, errors.New("invalid memory snapshot")
	}
	seen := map[string]bool{}
	for _, e := range snap.Entries {
		if !storage.ValidID(e.ID) || seen[e.ID] || !valid(e.Content, e.Status) || e.Revision < 1 {
			return nil, errors.New("invalid memory entry")
		}
		seen[e.ID] = true
	}
	s.cache[role] = snap.Entries
	return append([]Entry(nil), snap.Entries...), nil
}
func (s *Store) save(role string, entries []Entry) error {
	data, err := json.MarshalIndent(snapshot{Version: 1, RoleID: role, Entries: entries}, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > 4<<20 {
		return errors.New("memory file too large")
	}
	if err := storage.Replace(filepath.Join(s.dir, role+".json"), append(data, '\n')); err != nil {
		return err
	}
	s.cache[role] = entries
	return nil
}
func (s *Store) List(role string) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.load(role)
	if err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []Entry{}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].UpdatedAt.After(entries[j].UpdatedAt) })
	return entries, nil
}
func (s *Store) Create(role, content, status, source, sessionID string) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	content = strings.TrimSpace(content)
	if !valid(content, status) {
		return Entry{}, errors.New("memory must contain 1–4096 bytes and a valid status")
	}
	entries, err := s.load(role)
	if err != nil {
		return Entry{}, err
	}
	for _, entry := range entries {
		if entry.Content == content {
			return entry, nil
		}
	}
	if len(entries) >= 500 {
		return Entry{}, errors.New("role memory limit reached")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Entry{}, err
	}
	now := time.Now().UTC()
	entry := Entry{ID: hex.EncodeToString(id[:]), Content: content, Status: status, Source: source, SessionID: sessionID, Revision: 1, CreatedAt: now, UpdatedAt: now}
	entries = append(entries, entry)
	if err := s.save(role, entries); err != nil {
		return Entry{}, err
	}
	return entry, nil
}
func (s *Store) Update(role, id, content, status string, revision int) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	content = strings.TrimSpace(content)
	if !valid(content, status) {
		return Entry{}, errors.New("invalid memory content or status")
	}
	entries, err := s.load(role)
	if err != nil {
		return Entry{}, err
	}
	for i, e := range entries {
		if e.ID == id {
			if revision != e.Revision {
				return Entry{}, ErrConflict
			}
			e.Content = content
			e.Status = status
			e.Revision++
			e.UpdatedAt = time.Now().UTC()
			entries[i] = e
			if err := s.save(role, entries); err != nil {
				return Entry{}, err
			}
			return e, nil
		}
	}
	return Entry{}, ErrNotFound
}
func (s *Store) Delete(role, id string, revision int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.load(role)
	if err != nil {
		return err
	}
	for i, e := range entries {
		if e.ID == id {
			if revision != e.Revision {
				return ErrConflict
			}
			entries = append(entries[:i:i], entries[i+1:]...)
			return s.save(role, entries)
		}
	}
	return ErrNotFound
}

func (s *Store) Get(role, id string) (Entry, error) {
	entries, err := s.List(role)
	if err != nil {
		return Entry{}, err
	}
	for _, e := range entries {
		if e.ID == id {
			return e, nil
		}
	}
	return Entry{}, ErrNotFound
}

// Merge keeps the first record's identity and review state. All revisions and
// source metadata are checked before one file replacement publishes the merge.
func (s *Store) Merge(role string, refs []Reference, content string) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(refs) < 2 || len(refs) > 8 {
		return Entry{}, errors.New("merge requires 2–8 records")
	}
	entries, err := s.load(role)
	if err != nil {
		return Entry{}, err
	}
	selected := map[string]Entry{}
	var first Entry
	for i, ref := range refs {
		if _, ok := selected[ref.ID]; ok {
			return Entry{}, ErrConflict
		}
		found := false
		for _, e := range entries {
			if e.ID == ref.ID {
				if e.Revision != ref.Revision {
					return Entry{}, ErrConflict
				}
				if i == 0 {
					first = e
				} else if e.Status != first.Status {
					return Entry{}, errors.New("merge cannot change review state")
				}
				selected[e.ID] = e
				found = true
				break
			}
		}
		if !found {
			return Entry{}, ErrNotFound
		}
	}
	content = strings.TrimSpace(content)
	if !valid(content, first.Status) {
		return Entry{}, errors.New("invalid merged content")
	}
	type origin struct {
		ID        string `json:"id"`
		Source    string `json:"source"`
		SessionID string `json:"session_id,omitempty"`
	}
	origins := []origin{}
	seen := map[string]bool{}
	for _, ref := range refs {
		e := selected[ref.ID]
		parts := []origin{{ID: e.ID, Source: e.Source, SessionID: e.SessionID}}
		if strings.HasPrefix(e.Source, "merged:") {
			var prior []origin
			if json.Unmarshal([]byte(strings.TrimPrefix(e.Source, "merged:")), &prior) == nil {
				parts = prior
			}
		}
		for _, part := range parts {
			if !seen[part.ID] {
				seen[part.ID] = true
				origins = append(origins, part)
			}
		}
	}
	trace, _ := json.Marshal(origins)
	if len(origins) > 32 || len(trace) > 4096 {
		return Entry{}, errors.New("merged source limit reached")
	}
	first.Content = content
	first.Source = "merged:" + string(trace)
	first.Revision++
	first.UpdatedAt = time.Now().UTC()
	next := []Entry{}
	for _, e := range entries {
		if e.ID == first.ID {
			next = append(next, first)
		} else if _, ok := selected[e.ID]; !ok {
			next = append(next, e)
		}
	}
	if err := s.save(role, next); err != nil {
		return Entry{}, err
	}
	return first, nil
}

// Search deliberately excludes pending entries. Chinese bigrams and Latin words
// provide a bounded keyword baseline without a tokenizer or vector dependency.
func (s *Store) Search(role, query string, limit int) ([]Entry, error) {
	return s.Find(role, query, "approved", limit)
}

func (s *Store) SearchContext(ctx context.Context, role, query string, limit int) ([]Entry, error) {
	return s.FindContext(ctx, role, query, "approved", limit)
}

// Find exposes review status for explicit management, while Search continues
// to return approved facts only for ordinary automatic context assembly.
func (s *Store) Find(role, query, status string, limit int) ([]Entry, error) {
	return s.FindContext(context.Background(), role, query, status, limit)
}

func (s *Store) FindContext(ctx context.Context, role, query, status string, limit int) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if status == "" {
		status = "approved"
	}
	if status != "approved" && status != "pending" && status != "all" {
		return nil, errors.New("invalid memory status")
	}
	entries, err := s.List(role)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 20 {
		limit = 5
	}
	selected := []Entry{}
	documents := []retrieval.Document{}
	for _, entry := range entries {
		if status != "all" && entry.Status != status {
			continue
		}
		selected = append(selected, entry)
		documents = append(documents, retrieval.Document{ID: entry.ID, Text: entry.Content, UpdatedAt: entry.UpdatedAt})
	}
	out := []Entry{}
	seen := map[int]bool{}
	s.mu.Lock()
	reranker := s.reranker
	s.mu.Unlock()
	if reranker != nil && query != "" && len(documents) >= 2 {
		texts := make([]string, len(documents))
		for i, document := range documents {
			texts[i] = document.Text
		}
		var order []int
		var ok bool
		if contextual, yes := reranker.(ContextReranker); yes {
			order, ok = contextual.RerankContext(ctx, query, texts, limit)
		} else {
			order, ok = reranker.Rerank(query, texts, limit)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if ok {
			for _, index := range order {
				if index >= 0 && index < len(selected) && !seen[index] {
					seen[index] = true
					out = append(out, selected[index])
				}
			}
		}
	}
	// Keyword matches always survive, so the semantic layer only adds recall and
	// can never drop a record the local ranking already found.
	for _, match := range retrieval.Rank(query, documents, limit) {
		if !seen[match.Index] {
			seen[match.Index] = true
			out = append(out, selected[match.Index])
		}
		if len(out) >= limit {
			break
		}
	}
	return out[:min(len(out), limit)], nil
}
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return os.Remove(s.lockPath)
}
