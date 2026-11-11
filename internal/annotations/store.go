// Package annotations persists user-authored notes bound to an immutable
// conversation message identity and an exact UTF-8 byte range.
package annotations

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"chat-agent/internal/storage"
)

var (
	ErrInvalid  = errors.New("invalid annotation")
	ErrNotFound = errors.New("annotation not found")
	ErrConflict = errors.New("annotation revision conflict")
)

const Version = 1

// Item is a durable source binding. Start and End are UTF-8 byte offsets into
// the original message Content; SourceSHA256 covers the whole message and
// QuoteSHA256 covers the selected bytes.
type Item struct {
	ID           string    `json:"id"`
	SessionID    string    `json:"session_id"`
	RoleID       string    `json:"role_id"`
	MessageID    string    `json:"message_id"`
	MessageRole  string    `json:"message_role"`
	Start        int       `json:"start"`
	End          int       `json:"end"`
	OffsetUnit   string    `json:"offset_unit"`
	Quote        string    `json:"quote"`
	SourceSHA256 string    `json:"source_sha256"`
	QuoteSHA256  string    `json:"quote_sha256"`
	Note         string    `json:"note"`
	Revision     int       `json:"revision"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type Source struct {
	SessionID, RoleID, MessageID, MessageRole string
	Start, End                                int
	Quote, SourceSHA256, QuoteSHA256          string
}

type Store struct {
	root string
	mu   sync.Mutex
}

type file struct {
	Version int    `json:"version"`
	Items   []Item `json:"items"`
}

func Open(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, ErrInvalid
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

func (s *Store) List(sessionID, roleID string) ([]Item, error) {
	if !storage.ValidID(sessionID) || !storage.ValidID(roleID) {
		return nil, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load(sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(doc.Items))
	for _, item := range doc.Items {
		if item.RoleID == roleID {
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (s *Store) Create(source Source, note string, now time.Time) (Item, error) {
	if !validSource(source) || !validNote(note) || now.IsZero() {
		return Item{}, ErrInvalid
	}
	id, err := newID()
	if err != nil {
		return Item{}, err
	}
	item := Item{ID: id, SessionID: source.SessionID, RoleID: source.RoleID,
		MessageID: source.MessageID, MessageRole: source.MessageRole,
		Start: source.Start, End: source.End, OffsetUnit: "utf8_bytes", Quote: source.Quote,
		SourceSHA256: source.SourceSHA256, QuoteSHA256: source.QuoteSHA256,
		Note: strings.TrimSpace(note), Revision: 1, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load(source.SessionID)
	if err != nil {
		return Item{}, err
	}
	if len(doc.Items) >= 512 {
		return Item{}, ErrConflict
	}
	doc.Items = append(doc.Items, item)
	if err = s.save(source.SessionID, doc); err != nil {
		return Item{}, err
	}
	return item, nil
}

func (s *Store) Update(sessionID, roleID, id, note string, revision int, now time.Time) (Item, error) {
	if !storage.ValidID(sessionID) || !storage.ValidID(roleID) || !storage.ValidID(id) || !validNote(note) || revision < 1 || now.IsZero() {
		return Item{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load(sessionID)
	if err != nil {
		return Item{}, err
	}
	for i := range doc.Items {
		item := &doc.Items[i]
		if item.ID != id || item.RoleID != roleID {
			continue
		}
		if item.Revision != revision {
			return Item{}, ErrConflict
		}
		item.Note, item.Revision, item.UpdatedAt = strings.TrimSpace(note), item.Revision+1, now.UTC()
		if err = s.save(sessionID, doc); err != nil {
			return Item{}, err
		}
		return *item, nil
	}
	return Item{}, ErrNotFound
}

func (s *Store) Delete(sessionID, roleID, id string, revision int) error {
	if !storage.ValidID(sessionID) || !storage.ValidID(roleID) || !storage.ValidID(id) || revision < 1 {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load(sessionID)
	if err != nil {
		return err
	}
	for i, item := range doc.Items {
		if item.ID != id || item.RoleID != roleID {
			continue
		}
		if item.Revision != revision {
			return ErrConflict
		}
		doc.Items = append(doc.Items[:i], doc.Items[i+1:]...)
		return s.save(sessionID, doc)
	}
	return ErrNotFound
}

func (s *Store) load(sessionID string) (file, error) {
	doc := file{Version: Version, Items: []Item{}}
	raw, err := os.ReadFile(s.path(sessionID))
	if errors.Is(err, os.ErrNotExist) {
		return doc, nil
	}
	if err != nil {
		return file{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var extra any
	if decoder.Decode(&doc) != nil || decoder.Decode(&extra) != io.EOF || doc.Version != Version || len(doc.Items) > 512 {
		return file{}, errors.New("damaged annotation store")
	}
	seen := map[string]bool{}
	for _, item := range doc.Items {
		source := Source{SessionID: item.SessionID, RoleID: item.RoleID, MessageID: item.MessageID, MessageRole: item.MessageRole, Start: item.Start, End: item.End, Quote: item.Quote, SourceSHA256: item.SourceSHA256, QuoteSHA256: item.QuoteSHA256}
		if !storage.ValidID(item.ID) || item.SessionID != sessionID || !validSource(source) || !validNote(item.Note) || item.Revision < 1 || item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() || item.OffsetUnit != "utf8_bytes" || seen[item.ID] {
			return file{}, errors.New("damaged annotation store")
		}
		seen[item.ID] = true
	}
	return doc, nil
}

func (s *Store) save(sessionID string, doc file) error {
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return storage.Replace(s.path(sessionID), raw)
}

func (s *Store) path(sessionID string) string { return filepath.Join(s.root, sessionID+".json") }

func validSource(source Source) bool {
	return storage.ValidID(source.SessionID) && storage.ValidID(source.RoleID) && storage.ValidID(source.MessageID) &&
		(source.MessageRole == "user" || source.MessageRole == "assistant") && source.Start >= 0 && source.End > source.Start && source.End-source.Start <= 4096 &&
		utf8.ValidString(source.Quote) && len(source.Quote) == source.End-source.Start && validDigest(source.SourceSHA256) && validDigest(source.QuoteSHA256)
}

func validNote(note string) bool { return utf8.ValidString(note) && len(note) <= 8<<10 }

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func newID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "ann_" + hex.EncodeToString(raw[:]), nil
}
