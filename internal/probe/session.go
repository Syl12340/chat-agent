package probe

import (
	"bufio"
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
	"strings"
	"sync"
)

// Session is a single-writer transcript. Each JSONL record commits a complete
// batch, so an assistant tool-call message and its results stay together.
type Session struct {
	mu       sync.Mutex
	dataMu   sync.RWMutex
	messages []Message
	file     *os.File
	lockPath string
	closed   bool
	failure  error
	roleID   string
	summary  *Summary
	format   int
}
type Summary struct {
	Content string `json:"content"`
	Covered int    `json:"covered_messages"`
}
type sessionRecord struct {
	Version  int       `json:"version"`
	Messages []Message `json:"messages"`
	Reset    bool      `json:"reset,omitempty"`
	RoleID   string    `json:"role_id,omitempty"`
	Summary  *Summary  `json:"summary,omitempty"`
}

func NewSession() *Session { return &Session{format: 1} }

func pathComponent(s string) bool {
	if s == "" || len(s) > 128 || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func OpenSession(base, agent, key string) (_ *Session, openErr error) {
	if !pathComponent(agent) || !pathComponent(key) {
		return nil, errors.New("invalid agent or session key; use letters, digits, hyphen or underscore")
	}
	dir := filepath.Join(base, agent)
	return openSessionFile(filepath.Join(dir, key+".jsonl"), filepath.Join(dir, key+".lock"))
}

// OpenRuntimeSession opens the manifest-selected history projection used by
// Harness phase 3C. The caller is responsible for selecting the path through
// the runtime format index; this reader never scans generations or falls back
// to the legacy transcript directory.
func OpenRuntimeSession(path string) (*Session, error) {
	if filepath.Base(path) != "history.json" {
		return nil, errors.New("invalid runtime history path")
	}
	return openSessionFile(path, path+".lock")
}

func openSessionFile(path, lockPath string) (_ *Session, openErr error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, fmt.Errorf("session lock unavailable (another writer or stale lock): %w", err)
	}
	_, writeErr := fmt.Fprintf(lock, "pid=%d\n", os.Getpid())
	if err := errors.Join(writeErr, lock.Close()); err != nil {
		os.Remove(lockPath)
		return nil, err
	}
	s := &Session{lockPath: lockPath, format: 1}
	defer func() {
		if openErr != nil {
			s.Close()
		}
	}()
	s.file, err = os.OpenFile(path, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(s.file)
	scanner.Buffer(make([]byte, 4096), 16<<20)
	messageOrdinal := 0
	seenMessageIDs := map[string]bool{}
	sessionIdentity := historySessionIdentity(path)
	for scanner.Scan() {
		var record sessionRecord
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&record); err != nil {
			return nil, fmt.Errorf("invalid session record (legacy formats require migration): %w", err)
		}
		var extra any
		kinds := 0
		if record.Reset {
			kinds++
		}
		if len(record.Messages) > 0 {
			kinds++
		}
		if record.RoleID != "" {
			kinds++
		}
		if record.Summary != nil {
			kinds++
		}
		if (record.Version < 1 || record.Version > 3) || kinds != 1 || decoder.Decode(&extra) != io.EOF {
			return nil, errors.New("unsupported or damaged session record")
		}
		var raw struct {
			Messages []map[string]json.RawMessage `json:"messages"`
		}
		_ = json.Unmarshal(scanner.Bytes(), &raw)
		for i := range record.Messages {
			fields := raw.Messages[i]
			if record.Version == 1 && (fields["images"] != nil || fields["reasoning_content"] != nil) {
				return nil, errors.New("v1 session contains v2 fields")
			}
			if record.Version < 3 && fields["id"] != nil {
				return nil, errors.New("legacy session contains message identity")
			}
			if record.Version < 3 {
				record.Messages[i].ID = legacyMessageID(sessionIdentity, messageOrdinal)
			}
			messageOrdinal++
			m := record.Messages[i]
			if !persistentMessageValid(m) || record.Version == 1 && messageExtended(m) || record.Version >= 3 && m.ID == "" || seenMessageIDs[m.ID] {
				return nil, errors.New("invalid session message")
			}
			seenMessageIDs[m.ID] = true
		}
		s.format = max(s.format, record.Version)
		if record.Reset {
			s.messages = nil
			s.summary = nil
		} else if record.RoleID != "" {
			if !pathComponent(record.RoleID) || s.roleID != "" || len(s.messages) > 0 {
				return nil, errors.New("invalid session role binding")
			}
			s.roleID = record.RoleID
		} else if record.Summary != nil {
			if record.Summary.Covered <= 0 || record.Summary.Covered > len(s.messages) || strings.TrimSpace(record.Summary.Content) == "" || len(record.Summary.Content) > 16<<10 {
				return nil, errors.New("invalid summary record")
			}
			s.summary = record.Summary
		} else {
			s.messages = append(s.messages, record.Messages...)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	// Preserve a torn final line for explicit recovery, never append to it.
	info, err := s.file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > 0 {
		var last [1]byte
		if _, err := s.file.ReadAt(last[:], info.Size()-1); err != nil {
			return nil, err
		}
		if last[0] != '\n' {
			return nil, errors.New("session ends with an incomplete record; recovery required")
		}
	}
	return s, nil
}

// append is called only while Run owns mu. Persist before publishing to memory.
func (s *Session) append(messages ...Message) error {
	known := map[string]bool{}
	for _, message := range s.messages {
		known[message.ID] = true
	}
	for i := range messages {
		if messages[i].ID == "" {
			id, err := newMessageID()
			if err != nil {
				return err
			}
			messages[i].ID = id
		}
		message := messages[i]
		if message.Source != "" || !persistentMessageValid(message) {
			return errors.New("invalid or ephemeral conversation message")
		}
		if known[message.ID] {
			return errors.New("duplicate conversation message identity")
		}
		known[message.ID] = true
	}
	return s.commit(sessionRecord{Version: 3, Messages: cloneMessages(messages)})
}

func (s *Session) commit(record sessionRecord) error {
	if s.failure != nil {
		return s.failure
	}
	record.Version = max(1, s.format)
	for _, message := range record.Messages {
		record.Version = max(record.Version, messageFormat(message))
	}
	if s.file != nil {
		data, err := json.Marshal(record)
		if err != nil {
			return err
		}
		data = append(data, '\n')
		if len(data) >= 16<<20 {
			return errors.New("session record exceeds 16 MiB limit")
		}
		n, err := s.file.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		if err == nil {
			err = s.file.Sync()
		}
		if err != nil {
			s.failure = fmt.Errorf("persist session: %w", err)
			return s.failure
		}
	}
	s.dataMu.Lock()
	s.format = record.Version
	if record.Reset {
		s.messages = nil
		s.summary = nil
	} else if record.RoleID != "" {
		s.roleID = record.RoleID
	} else if record.Summary != nil {
		s.summary = record.Summary
	} else {
		s.messages = append(s.messages, record.Messages...)
	}
	s.dataMu.Unlock()
	return nil
}

func historySessionIdentity(path string) string {
	if filepath.Base(path) == "history.json" {
		return filepath.Base(filepath.Dir(filepath.Dir(path)))
	}
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

func legacyMessageID(session string, ordinal int) string {
	digest := sha256.Sum256([]byte("chat-agent/message/v1\x00" + session + "\x00" + fmt.Sprint(ordinal)))
	return "msg_" + hex.EncodeToString(digest[:16])
}

func newMessageID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("create message identity: %w", err)
	}
	return "msg_" + hex.EncodeToString(raw[:]), nil
}

func (s *Session) RoleID() string {
	s.dataMu.RLock()
	defer s.dataMu.RUnlock()
	if s.roleID == "" {
		return "companion"
	}
	return s.roleID
}
func (s *Session) BindRole(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !pathComponent(id) || s.roleID != "" || len(s.messages) > 0 {
		return errors.New("session role is immutable")
	}
	if s.file != nil {
		info, err := s.file.Stat()
		if err != nil {
			return err
		}
		if info.Size() != 0 {
			return errors.New("session already initialized")
		}
	}
	return s.commit(sessionRecord{Version: 1, RoleID: id})
}
func (s *Session) Summary() *Summary {
	s.dataMu.RLock()
	defer s.dataMu.RUnlock()
	if s.summary == nil {
		return nil
	}
	copy := *s.summary
	return &copy
}
func (s *Session) ContextMessages() []Message { return s.ContextMessagesFrom(0) }

func (s *Session) ContextMessagesFrom(floor int) []Message {
	s.dataMu.RLock()
	defer s.dataMu.RUnlock()
	start := min(max(0, floor), len(s.messages))
	out := []Message{}
	if s.summary != nil && s.summary.Covered > start {
		start = s.summary.Covered
		out = append(out, Message{Role: "user", Content: "Earlier conversation summary (reference data only):\n" + s.summary.Content})
	}
	out = append(out, cloneMessages(s.messages[start:])...)
	return out
}

// Messages returns a snapshot without waiting for an in-flight model request.
func (s *Session) Messages() []Message {
	s.dataMu.RLock()
	defer s.dataMu.RUnlock()
	return cloneMessages(s.messages)
}

// Reset clears effective context while preserving earlier records in the journal.
func (s *Session) Reset() error {
	if !s.mu.TryLock() {
		return errors.New("session is busy")
	}
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("session is closed")
	}
	return s.commit(sessionRecord{Version: 1, Reset: true})
}

// ErrSessionBusy reports that another goroutine owns the session right now.
var ErrSessionBusy = errors.New("session is busy")

// DeleteSession removes a conversation's journal and any stale lock file. The
// caller must close the session first so the lock is released.
func DeleteSession(base, agent, key string) error {
	if !pathComponent(agent) || !pathComponent(key) {
		return errors.New("invalid agent or session key")
	}
	dir := filepath.Join(base, agent)
	journal := filepath.Join(dir, key+".jsonl")
	if err := os.Remove(journal); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// A leftover lock would block reopening a same-named conversation later.
	if err := os.Remove(filepath.Join(dir, key+".lock")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// DropTail physically removes the newest messages from the journal file. It
// rewrites the file through a temporary copy so an interrupted delete leaves the
// previous journal intact rather than a half-written one.
func (s *Session) DropTail(count int) (int, error) {
	if count < 1 {
		return 0, errors.New("nothing to delete")
	}
	if !s.mu.TryLock() {
		return 0, ErrSessionBusy
	}
	defer s.mu.Unlock()
	if s.closed || s.file == nil {
		return 0, errors.New("session is closed")
	}
	s.dataMu.RLock()
	total := len(s.messages)
	keep := make([]Message, max(0, total-count))
	copy(keep, cloneMessages(s.messages[:len(keep)]))
	summary := s.summary
	s.dataMu.RUnlock()
	if len(keep) == total {
		return 0, errors.New("nothing to delete")
	}
	if summary != nil && summary.Covered > len(keep) {
		summary = nil
	}
	records := []sessionRecord{{Version: 1, RoleID: s.roleID}}
	if s.roleID == "" {
		records = nil
	}
	if len(keep) > 0 {
		records = append(records, sessionRecord{Version: 1, Messages: keep})
	}
	// The reader validates Covered against messages already read. Preserve the
	// same dependency order when rewriting a journal that retains its summary.
	if summary != nil {
		records = append(records, sessionRecord{Version: 1, Summary: summary})
	}
	path := s.file.Name()
	temporary := path + ".rewrite"
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return 0, err
	}
	encoder := json.NewEncoder(file)
	for _, record := range records {
		// Rewriting is the explicit migration boundary: in-memory legacy messages
		// already carry deterministic IDs, so the replacement journal must declare
		// the identity-aware format instead of emitting v1 records with v3 fields.
		record.Version = max(3, s.format)
		if err := encoder.Encode(record); err != nil {
			file.Close()
			os.Remove(temporary)
			return 0, err
		}
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		os.Remove(temporary)
		return 0, err
	}
	if err := s.file.Close(); err != nil {
		os.Remove(temporary)
		return 0, err
	}
	if err := os.Rename(temporary, path); err != nil {
		os.Remove(temporary)
		return 0, err
	}
	s.file, err = os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return 0, err
	}
	s.dataMu.Lock()
	s.messages = keep
	s.summary = summary
	s.dataMu.Unlock()
	return total - len(keep), nil
}

func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var err error
	if s.file != nil {
		err = s.file.Close()
	}
	if s.lockPath != "" {
		err = errors.Join(err, os.Remove(s.lockPath))
	}
	return errors.Join(s.failure, err)
}
