// Package turns records bounded, durable submissions independently of the chat
// journal. Unknown/interrupted model calls are never replayed automatically.
package turns

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"pocket-agent/internal/media"
	"pocket-agent/internal/storage"
)

var ErrConflict = errors.New("request already used or state changed")
var ErrLimit = errors.New("turn queue or ledger limit reached")
var ErrNotFound = errors.New("request not found")

type ToolEvent struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
	Output    string `json:"output,omitempty"`
	Error     string `json:"error,omitempty"`
}
type Turn struct {
	ID         string        `json:"id"`
	Sequence   int           `json:"sequence"`
	Content    string        `json:"content"`
	Mode       string        `json:"mode"`
	Status     string        `json:"status"`
	BatchID    string        `json:"batch_id,omitempty"`
	StartIndex int           `json:"start_index"`
	EndIndex   int           `json:"end_index"`
	Text       string        `json:"text,omitempty"`
	Tools      []ToolEvent   `json:"tools,omitempty"`
	Error      string        `json:"error,omitempty"`
	CreatedAt  time.Time     `json:"created_at"`
	Model      string        `json:"model,omitempty"`
	Thinking   string        `json:"thinking,omitempty"`
	Images     []media.Image `json:"images,omitempty"`
}
type record struct {
	Turn
	Digest string `json:"digest"`
}
type state struct {
	Version   int      `json:"version"`
	SessionID string   `json:"session_id"`
	Paused    bool     `json:"paused"`
	Items     []record `json:"items"`
}
type Store struct {
	mu         sync.Mutex
	dir, lock  string
	closed     bool
	cache      map[string]*state
	checkpoint map[string]time.Time
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock := filepath.Join(dir, ".store.lock")
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	_, err = fmt.Fprintf(f, "pid=%d\n", os.Getpid())
	err = errors.Join(err, f.Close())
	if err != nil {
		os.Remove(lock)
		return nil, err
	}
	return &Store{dir: dir, lock: lock, cache: map[string]*state{}, checkpoint: map[string]time.Time{}}, nil
}
func clone(st *state) *state {
	out := *st
	out.Items = append([]record(nil), st.Items...)
	for i := range out.Items {
		out.Items[i].Tools = append([]ToolEvent(nil), out.Items[i].Tools...)
		out.Items[i].Images = append([]media.Image(nil), out.Items[i].Images...)
	}
	return &out
}
func (s *Store) save(st *state) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if len(b) > 12<<20 {
		return ErrLimit
	}
	if err = storage.Replace(filepath.Join(s.dir, st.SessionID+".json"), b); err != nil {
		return err
	}
	s.cache[st.SessionID] = st
	s.checkpoint[st.SessionID] = time.Now()
	return nil
}
func (s *Store) load(id string) (*state, error) {
	if s.closed {
		return nil, errors.New("turn store closed")
	}
	if !storage.ValidID(id) {
		return nil, ErrNotFound
	}
	if st := s.cache[id]; st != nil {
		return st, nil
	}
	f, err := os.Open(filepath.Join(s.dir, id+".json"))
	if errors.Is(err, os.ErrNotExist) {
		st := &state{Version: 1, SessionID: id, Items: []record{}}
		s.cache[id] = st
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(f, (12<<20)+1))
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(b) > 12<<20 {
		return nil, ErrLimit
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	var st state
	var extra any
	if err = d.Decode(&st); err != nil {
		return nil, err
	}
	if d.Decode(&extra) != io.EOF || (st.Version != 1 && st.Version != 2) || st.SessionID != id || len(st.Items) > 2048 {
		return nil, errors.New("invalid turn ledger")
	}
	if st.Version == 1 {
		var raw struct {
			Items []map[string]json.RawMessage `json:"items"`
		}
		_ = json.Unmarshal(b, &raw)
		for _, item := range raw.Items {
			if item["model"] != nil || item["thinking"] != nil || item["images"] != nil {
				return nil, errors.New("v1 ledger contains v2 fields")
			}
		}
	}
	changed := false
	seen := map[string]bool{}
	for i := range st.Items {
		t := &st.Items[i]
		if !storage.ValidID(t.ID) || seen[t.ID] || t.Sequence != i+1 || len(t.Digest) != 64 || t.StartIndex < 0 || t.EndIndex < 0 || len(t.Content) > 64<<10 || len(t.Text) > 64<<10 {
			return nil, errors.New("invalid turn record")
		}
		if !validOptions(t.Model, t.Thinking, t.Images) {
			return nil, errors.New("invalid turn options")
		}
		seen[t.ID] = true
		switch t.Status {
		case "queued":
			st.Paused = true
			changed = true
		case "running":
			t.Status = "interrupted"
			t.Error = "服务曾中断；没有自动重新调用模型。"
			st.Paused = true
			changed = true
		case "completed", "canceled", "failed", "interrupted", "withdrawn":
		default:
			return nil, errors.New("invalid turn status")
		}
	}
	s.cache[id] = &st
	if changed {
		if err = s.save(&st); err != nil {
			delete(s.cache, id)
			return nil, err
		}
	}
	return &st, nil
}
func digest(content, mode string) string {
	b := sha256.Sum256([]byte(mode + "\x00" + content))
	return hex.EncodeToString(b[:])
}
func (s *Store) Enqueue(id, rid, content, mode string) (Turn, bool, error) {
	return s.EnqueueWithOptions(id, rid, content, mode, "", "", nil)
}
func (s *Store) EnqueueWithOptions(id, rid, content, mode, model, thinking string, images []media.Image) (Turn, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !storage.ValidID(rid) || strings.TrimSpace(content) == "" && len(images) == 0 || len(content) > 64<<10 || !validOptions(model, thinking, images) {
		return Turn{}, false, ErrConflict
	}
	st, err := s.load(id)
	if err != nil {
		return Turn{}, false, err
	}
	hash := optionsDigest(content, mode, model, thinking, images)
	pending := 0
	for _, r := range st.Items {
		if r.ID == rid {
			if r.Digest != hash {
				return Turn{}, false, ErrConflict
			}
			return cloneTurn(r.Turn), true, nil
		}
		if r.Status == "queued" {
			pending++
		}
	}
	if pending >= 8 || len(st.Items) >= 2048 {
		return Turn{}, false, ErrLimit
	}
	next := clone(st)
	turn := Turn{ID: rid, Sequence: len(next.Items) + 1, Content: content, Mode: mode, Status: "queued", CreatedAt: time.Now().UTC(), Model: model, Thinking: thinking, Images: append([]media.Image(nil), images...)}
	if model != "" || thinking != "" || len(images) > 0 {
		next.Version = 2
	}
	next.Items = append(next.Items, record{Turn: turn, Digest: hash})
	// Retain request digests for the entire bounded ledger; compact only payloads.
	for i := 0; i < len(next.Items)-32; i++ {
		if next.Items[i].Status != "queued" && next.Items[i].Status != "running" {
			next.Items[i].Content = ""
			next.Items[i].Text = ""
			next.Items[i].Tools = nil
		}
	}
	if err = s.save(next); err != nil {
		return Turn{}, false, err
	}
	return cloneTurn(turn), false, nil
}
func (s *Store) List(id string) ([]Turn, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.load(id)
	if err != nil {
		return nil, false, err
	}
	out := []Turn{}
	start := max(0, len(st.Items)-32)
	for _, r := range st.Items[start:] {
		t := cloneTurn(r.Turn)
		out = append(out, t)
	}
	return out, st.Paused, nil
}
func (s *Store) HasQueued(id string) (bool, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.load(id)
	if err != nil {
		return false, false, err
	}
	for _, r := range st.Items {
		if r.Status == "queued" {
			return true, st.Paused, nil
		}
	}
	return false, st.Paused, nil
}
func (s *Store) Pause(id string, paused bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.load(id)
	if err != nil {
		return err
	}
	if st.Paused == paused {
		return nil
	}
	next := clone(st)
	next.Paused = paused
	return s.save(next)
}

// Get returns one recorded turn without mutating the ledger.
func (s *Store) Get(id, rid string) (Turn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.load(id)
	if err != nil {
		return Turn{}, err
	}
	for _, r := range st.Items {
		if r.ID == rid {
			return cloneTurn(r.Turn), nil
		}
	}
	return Turn{}, ErrNotFound
}

// Remove drops one turn from the ledger. It refuses while the turn is running,
// because the worker still owns that record.
func (s *Store) Remove(id, rid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.load(id)
	if err != nil {
		return err
	}
	next := clone(st)
	kept := next.Items[:0]
	found := false
	for _, r := range next.Items {
		if r.ID != rid {
			kept = append(kept, r)
			continue
		}
		if r.Status == "running" {
			return ErrConflict
		}
		found = true
	}
	if !found {
		return ErrNotFound
	}
	next.Items = kept
	for i := range next.Items {
		next.Items[i].Sequence = i + 1
	}
	return s.save(next)
}

// Delete removes a whole session ledger, used when its conversation is deleted.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cache, id)
	err := os.Remove(filepath.Join(s.dir, id+".json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *Store) Withdraw(id, rid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.load(id)
	if err != nil {
		return err
	}
	next := clone(st)
	for i := range next.Items {
		r := &next.Items[i]
		if r.ID == rid {
			if r.Status == "withdrawn" {
				return nil
			}
			if r.Status != "queued" {
				return ErrConflict
			}
			r.Status = "withdrawn"
			return s.save(next)
		}
	}
	return ErrNotFound
}
func (s *Store) Begin(id string, start int, merge func(Turn) bool) ([]Turn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.load(id)
	if err != nil {
		return nil, err
	}
	if st.Paused {
		return nil, nil
	}
	next := clone(st)
	out := []Turn{}
	size, imageCount := 0, 0
	mode := ""
	for i := range next.Items {
		r := &next.Items[i]
		if r.Status != "queued" {
			continue
		}
		if len(out) > 0 && (len(out) >= 4 || size+len(r.Content) > 64<<10 || r.Mode != mode || r.Model != out[0].Model || r.Thinking != out[0].Thinking || imageCount+len(r.Images) > 3 || !merge(cloneTurn(r.Turn)) || !merge(cloneTurn(out[0]))) {
			break
		}
		if len(out) == 0 {
			mode = r.Mode
		}
		r.Status = "running"
		r.StartIndex = start
		r.BatchID = r.ID
		if len(out) > 0 {
			r.BatchID = out[0].ID
		}
		out = append(out, cloneTurn(r.Turn))
		size += len(r.Content)
		imageCount += len(r.Images)
	}
	if len(out) == 0 {
		return nil, nil
	}
	if err = s.save(next); err != nil {
		return nil, err
	}
	return out, nil
}
func (s *Store) Progress(id, batch, text string, tools []ToolEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(text) > 64<<10 {
		return ErrLimit
	}
	st, err := s.load(id)
	if err != nil {
		return err
	}
	next := clone(st)
	for i := range next.Items {
		r := &next.Items[i]
		if r.ID == batch && r.Status == "running" {
			r.Text = text
			r.Tools = append([]ToolEvent(nil), tools...)
			if time.Since(s.checkpoint[id]) >= time.Second {
				return s.save(next)
			}
			s.cache[id] = next
			return nil
		}
	}
	return ErrConflict
}
func (s *Store) Finish(id, batch, status, text, message string, end int, tools []ToolEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.load(id)
	if err != nil {
		return err
	}
	next := clone(st)
	for i := range next.Items {
		r := &next.Items[i]
		if r.BatchID == batch && r.Status == "running" {
			r.Status = status
			r.EndIndex = end
			r.Error = message
			if r.ID == batch {
				r.Text = text
				r.Tools = append([]ToolEvent(nil), tools...)
			}
		}
	}
	if status != "completed" {
		next.Paused = true
	}
	return s.save(next)
}
func (s *Store) Reset(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.load(id)
	if err != nil {
		return err
	}
	next := clone(st)
	next.Paused = false
	for i := range next.Items {
		r := &next.Items[i]
		r.Status = "withdrawn"
		r.Content = ""
		r.Text = ""
		r.Tools = nil
		r.Error = ""
		r.BatchID = ""
		r.StartIndex = 0
		r.EndIndex = 0
	}
	return s.save(next)
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
