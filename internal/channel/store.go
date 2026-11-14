package channel

import (
	"bytes"
	"crypto/sha256"
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

	"chat-agent/internal/storage"
)

const (
	storeVersion = 1
	storeLimit   = 4096
	storeMaxSize = 32 << 20
)

var (
	ErrConflict = errors.New("channel identity or payload conflict")
	ErrClosed   = errors.New("channel store closed")
	ErrLimit    = errors.New("channel store limit reached")
	ErrNotFound = errors.New("channel record not found")
)

type diskState struct {
	Version  int                     `json:"version"`
	Mappings map[string]Mapping      `json:"mappings"`
	Inbox    map[string]InboxRecord  `json:"inbox"`
	Outbox   map[string]OutboxRecord `json:"outbox"`
}

type Store struct {
	mu     sync.Mutex
	dir    string
	lock   string
	state  diskState
	closed bool
}

type DeliveryIntent struct {
	Platform         string
	AccountID        string
	ConversationKind string
	SessionID        string
	RequestID        string
	TargetID         string
	ReplyTo          string
	Text             string
	Proactive        bool
}

func deliveryIdentity(intent DeliveryIntent, platform, kind string) string {
	// Preserve the deployed OneBot P0-P2 identity exactly. Reconciliation after
	// an upgrade must find the old settled record rather than sending it again.
	if platform == "onebot-v11" && intent.AccountID == "" && kind == "private" && intent.ReplyTo == "" && !intent.Proactive {
		return "onebot-delivery:" + intent.SessionID + ":" + intent.RequestID + ":" + intent.TargetID
	}
	return strings.Join([]string{"channel-delivery-v2", platform, intent.AccountID, kind, intent.SessionID, intent.RequestID, intent.TargetID, intent.ReplyTo}, "\x00")
}

func OpenStore(dir string) (_ *Store, err error) {
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock := filepath.Join(dir, ".store.lock")
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		_ = os.Remove(lock)
		return nil, err
	}
	s := &Store{dir: dir, lock: lock, state: diskState{Version: storeVersion, Mappings: map[string]Mapping{}, Inbox: map[string]InboxRecord{}, Outbox: map[string]OutboxRecord{}}}
	defer func() {
		if err != nil {
			_ = s.Close()
		}
	}()
	data, readErr := os.ReadFile(filepath.Join(dir, "state.json"))
	if errors.Is(readErr, os.ErrNotExist) {
		return s, nil
	}
	if readErr != nil {
		return nil, readErr
	}
	if len(data) > storeMaxSize || decodeState(data, &s.state) != nil || !validState(s.state) {
		return nil, errors.New("invalid channel store")
	}
	changed := false
	now := time.Now().UTC()
	for key, record := range s.state.Inbox {
		if record.State == "accepted" {
			record.State = "needs_attention"
			record.UpdatedAt = now
			s.state.Inbox[key] = record
			changed = true
		}
	}
	for key, record := range s.state.Outbox {
		if record.State == "sending" {
			record.State = "unknown"
			record.UpdatedAt = now
			s.state.Outbox[key] = record
			changed = true
		}
	}
	if changed {
		if err = s.saveLocked(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func decodeState(data []byte, state *diskState) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(state); err != nil {
		return err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("trailing channel state")
	}
	return nil
}

func validState(state diskState) bool {
	if state.Version != storeVersion || state.Mappings == nil || state.Inbox == nil || state.Outbox == nil || len(state.Mappings) > storeLimit || len(state.Inbox) > storeLimit || len(state.Outbox) > storeLimit {
		return false
	}
	for key, mapping := range state.Mappings {
		want, err := MappingID(mapping.Key)
		if err != nil || key != want || !storage.ValidID(mapping.SessionID) || (mapping.State != "provisioning" && mapping.State != "active") || mapping.UpdatedAt.IsZero() {
			return false
		}
	}
	for key, record := range state.Inbox {
		if key != record.EventID || !storage.ValidID(key) || !storage.ValidID(record.RequestID) || len(record.PayloadSHA256) != 64 || record.ReceivedAt.IsZero() || record.UpdatedAt.IsZero() {
			return false
		}
		switch record.State {
		case "accepted", "needs_attention":
			if record.Envelope == nil || record.Envelope.Validate() != nil {
				return false
			}
		case "queued", "completed", "failed":
		default:
			return false
		}
	}
	for key, record := range state.Outbox {
		if key != record.DeliveryID || !storage.ValidID(key) || !storage.ValidID(record.SessionID) || !storage.ValidID(record.RequestID) || record.TargetID == "" || len(record.ContentSHA256) != 64 || record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() {
			return false
		}
		switch record.State {
		case "pending", "sending", "unknown":
			if strings.TrimSpace(record.Text) == "" || len(record.Text) > 64<<10 {
				return false
			}
		case "sent", "failed":
		default:
			return false
		}
	}
	return true
}

func (s *Store) saveLocked() error {
	if s.closed {
		return ErrClosed
	}
	data, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	if len(data) > storeMaxSize {
		return ErrLimit
	}
	return storage.Replace(filepath.Join(s.dir, "state.json"), data)
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

func (s *Store) Accept(envelope Envelope, requestID string) (InboxRecord, bool, error) {
	if envelope.Validate() != nil || !storage.ValidID(requestID) || envelope.EventID != requestID {
		return InboxRecord{}, false, ErrInvalidEnvelope
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return InboxRecord{}, false, ErrClosed
	}
	if existing, ok := s.state.Inbox[envelope.EventID]; ok {
		if existing.PayloadSHA256 != envelope.PayloadSHA256 || existing.RequestID != requestID {
			return InboxRecord{}, false, ErrConflict
		}
		return existing, true, nil
	}
	if len(s.state.Inbox) >= storeLimit {
		return InboxRecord{}, false, ErrLimit
	}
	now := time.Now().UTC()
	copyEnvelope := envelope
	record := InboxRecord{Version: storeVersion, EventID: envelope.EventID, RequestID: requestID, Platform: envelope.Platform, AccountID: envelope.AccountID, Conversation: envelope.Conversation.Kind, TargetID: envelope.Conversation.ID, ExternalID: envelope.Message.ID, PayloadSHA256: envelope.PayloadSHA256, State: "accepted", ReceivedAt: envelope.ReceivedAt, UpdatedAt: now, Envelope: &copyEnvelope}
	s.state.Inbox[record.EventID] = record
	if err := s.saveLocked(); err != nil {
		delete(s.state.Inbox, record.EventID)
		return InboxRecord{}, false, err
	}
	return record, false, nil
}

func (s *Store) SetInboxState(eventID, state, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	record, ok := s.state.Inbox[eventID]
	if !ok {
		return ErrNotFound
	}
	if !ValidInboxTransition(record.State, state) || (sessionID != "" && !storage.ValidID(sessionID)) {
		return ErrConflict
	}
	previous := record
	record.State = state
	if sessionID != "" {
		record.SessionID = sessionID
	}
	record.UpdatedAt = time.Now().UTC()
	if state == "queued" || state == "completed" || state == "failed" {
		record.Envelope = nil
	}
	s.state.Inbox[eventID] = record
	if err := s.saveLocked(); err != nil {
		s.state.Inbox[eventID] = previous
		return err
	}
	return nil
}

func (s *Store) CompleteRequests(sessionID string, requestIDs []string) error {
	return s.finishRequests(sessionID, requestIDs, "completed")
}

func (s *Store) FailRequests(sessionID string, requestIDs []string) error {
	return s.finishRequests(sessionID, requestIDs, "failed")
}

func (s *Store) finishRequests(sessionID string, requestIDs []string, state string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	previous := make(map[string]InboxRecord)
	changed := false
	now := time.Now().UTC()
	for key, record := range s.state.Inbox {
		if record.SessionID != sessionID || record.State != "queued" {
			continue
		}
		for _, requestID := range requestIDs {
			if record.RequestID == requestID {
				previous[key] = record
				record.State = state
				record.UpdatedAt = now
				s.state.Inbox[key] = record
				changed = true
				break
			}
		}
	}
	if !changed {
		return nil
	}
	if err := s.saveLocked(); err != nil {
		for key, record := range previous {
			s.state.Inbox[key] = record
		}
		return err
	}
	return nil
}

func (s *Store) QueuedInbox() []InboxRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	items := make([]InboxRecord, 0)
	for _, record := range s.state.Inbox {
		if record.State == "queued" {
			items = append(items, record)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ReceivedAt.Before(items[j].ReceivedAt) })
	return items
}

func (s *Store) ProvisionMapping(key ConversationKey, sessionID string) (Mapping, bool, error) {
	id, err := MappingID(key)
	if err != nil || !storage.ValidID(sessionID) {
		return Mapping{}, false, ErrInvalidEnvelope
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Mapping{}, false, ErrClosed
	}
	if existing, ok := s.state.Mappings[id]; ok {
		return existing, false, nil
	}
	if len(s.state.Mappings) >= storeLimit {
		return Mapping{}, false, ErrLimit
	}
	mapping := Mapping{Key: key, SessionID: sessionID, State: "provisioning", UpdatedAt: time.Now().UTC()}
	s.state.Mappings[id] = mapping
	if err := s.saveLocked(); err != nil {
		delete(s.state.Mappings, id)
		return Mapping{}, false, err
	}
	return mapping, true, nil
}

func (s *Store) ActivateMapping(key ConversationKey, sessionID string) error {
	id, err := MappingID(key)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mapping, ok := s.state.Mappings[id]
	if s.closed {
		return ErrClosed
	}
	if !ok || mapping.SessionID != sessionID || (mapping.State != "provisioning" && mapping.State != "active") {
		return ErrConflict
	}
	if mapping.State == "active" {
		return nil
	}
	previous := mapping
	mapping.State = "active"
	mapping.UpdatedAt = time.Now().UTC()
	s.state.Mappings[id] = mapping
	if err := s.saveLocked(); err != nil {
		s.state.Mappings[id] = previous
		return err
	}
	return nil
}

func (s *Store) MappingForSession(sessionID string) (Mapping, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Mapping{}, false
	}
	for _, mapping := range s.state.Mappings {
		if mapping.SessionID == sessionID && mapping.State == "active" {
			return mapping, true
		}
	}
	return Mapping{}, false
}

func (s *Store) Mapping(key ConversationKey) (Mapping, bool) {
	id, err := MappingID(key)
	if err != nil {
		return Mapping{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mapping, ok := s.state.Mappings[id]
	return mapping, ok && !s.closed && mapping.State == "active"
}

func (s *Store) Mappings() []Mapping {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	items := make([]Mapping, 0, len(s.state.Mappings))
	for _, mapping := range s.state.Mappings {
		if mapping.State == "active" {
			items = append(items, mapping)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].UpdatedAt.After(items[j].UpdatedAt) })
	return items
}

// RelinkMapping moves future provider messages to an existing CA session. It
// does not copy or delete either transcript, so handoff is explicit and safe.
func (s *Store) RelinkMapping(key ConversationKey, sessionID string) error {
	id, err := MappingID(key)
	if err != nil || !storage.ValidID(sessionID) {
		return ErrInvalidEnvelope
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	mapping, ok := s.state.Mappings[id]
	if !ok || mapping.State != "active" {
		return ErrNotFound
	}
	previous := mapping
	mapping.SessionID = sessionID
	mapping.UpdatedAt = time.Now().UTC()
	s.state.Mappings[id] = mapping
	if err := s.saveLocked(); err != nil {
		s.state.Mappings[id] = previous
		return err
	}
	return nil
}

// InboxForRequest proves that a completed turn originated from this channel.
// App-originated turns in a channel-bound session therefore never leak out.
func (s *Store) InboxForRequest(sessionID, requestID string) (InboxRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return InboxRecord{}, false
	}
	for _, record := range s.state.Inbox {
		if record.SessionID == sessionID && record.RequestID == requestID {
			return record, true
		}
	}
	return InboxRecord{}, false
}

func (s *Store) CreateOutbox(sessionID, requestID, targetID, text string) (OutboxRecord, bool, error) {
	return s.CreateDelivery(DeliveryIntent{Platform: "onebot-v11", ConversationKind: "private", SessionID: sessionID, RequestID: requestID, TargetID: targetID, Text: text})
}

func (s *Store) CreateDelivery(intent DeliveryIntent) (OutboxRecord, bool, error) {
	platform := strings.TrimSpace(intent.Platform)
	if platform == "" {
		platform = "onebot-v11"
	}
	kind := strings.TrimSpace(intent.ConversationKind)
	if kind == "" {
		kind = "private"
	}
	sessionID, requestID, targetID, text := intent.SessionID, intent.RequestID, intent.TargetID, intent.Text
	if !storage.ValidID(sessionID) || !storage.ValidID(requestID) || strings.TrimSpace(targetID) == "" || strings.TrimSpace(text) == "" || len(text) > 64<<10 {
		return OutboxRecord{}, false, ErrInvalidEnvelope
	}
	digest := sha256.Sum256([]byte(text))
	contentDigest := hex.EncodeToString(digest[:])
	deliveryDigest := sha256.Sum256([]byte(deliveryIdentity(intent, platform, kind)))
	deliveryID := hex.EncodeToString(deliveryDigest[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return OutboxRecord{}, false, ErrClosed
	}
	if existing, ok := s.state.Outbox[deliveryID]; ok {
		if existing.ContentSHA256 != contentDigest || existing.TargetID != targetID {
			return OutboxRecord{}, false, ErrConflict
		}
		return existing, true, nil
	}
	if len(s.state.Outbox) >= storeLimit {
		return OutboxRecord{}, false, ErrLimit
	}
	now := time.Now().UTC()
	record := OutboxRecord{Version: storeVersion, DeliveryID: deliveryID, SessionID: sessionID, RequestID: requestID, Platform: platform, AccountID: intent.AccountID, Conversation: kind, TargetID: targetID, ReplyTo: intent.ReplyTo, Proactive: intent.Proactive, ContentSHA256: contentDigest, Text: text, State: "pending", CreatedAt: now, UpdatedAt: now}
	s.state.Outbox[deliveryID] = record
	if err := s.saveLocked(); err != nil {
		delete(s.state.Outbox, deliveryID)
		return OutboxRecord{}, false, err
	}
	return record, false, nil
}

// SettleCompletion atomically records the delivery intent and marks any
// matching channel inbox records completed. The dispatcher cannot observe the
// pending outbox before the inbox settlement is durable.
func (s *Store) SettleCompletion(sessionID, requestID, targetID, text string, requestIDs []string) (OutboxRecord, bool, error) {
	return s.SettleDelivery(DeliveryIntent{Platform: "onebot-v11", ConversationKind: "private", SessionID: sessionID, RequestID: requestID, TargetID: targetID, Text: text}, requestIDs)
}

func (s *Store) SettleDelivery(intent DeliveryIntent, requestIDs []string) (OutboxRecord, bool, error) {
	platform := strings.TrimSpace(intent.Platform)
	if platform == "" {
		platform = "onebot-v11"
	}
	kind := strings.TrimSpace(intent.ConversationKind)
	if kind == "" {
		kind = "private"
	}
	sessionID, requestID, targetID, text := intent.SessionID, intent.RequestID, intent.TargetID, intent.Text
	if !storage.ValidID(sessionID) || !storage.ValidID(requestID) || strings.TrimSpace(targetID) == "" || strings.TrimSpace(text) == "" || len(text) > 64<<10 {
		return OutboxRecord{}, false, ErrInvalidEnvelope
	}
	digest := sha256.Sum256([]byte(text))
	contentDigest := hex.EncodeToString(digest[:])
	deliveryDigest := sha256.Sum256([]byte(deliveryIdentity(intent, platform, kind)))
	deliveryID := hex.EncodeToString(deliveryDigest[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return OutboxRecord{}, false, ErrClosed
	}
	record, duplicate := s.state.Outbox[deliveryID]
	if duplicate {
		if record.ContentSHA256 != contentDigest || record.TargetID != targetID {
			return OutboxRecord{}, false, ErrConflict
		}
	} else {
		if len(s.state.Outbox) >= storeLimit {
			return OutboxRecord{}, false, ErrLimit
		}
		now := time.Now().UTC()
		record = OutboxRecord{Version: storeVersion, DeliveryID: deliveryID, SessionID: sessionID, RequestID: requestID, Platform: platform, AccountID: intent.AccountID, Conversation: kind, TargetID: targetID, ReplyTo: intent.ReplyTo, Proactive: intent.Proactive, ContentSHA256: contentDigest, Text: text, State: "pending", CreatedAt: now, UpdatedAt: now}
		s.state.Outbox[deliveryID] = record
	}
	previousInbox := make(map[string]InboxRecord)
	now := time.Now().UTC()
	for key, inbox := range s.state.Inbox {
		if inbox.SessionID != sessionID || inbox.State != "queued" {
			continue
		}
		for _, id := range requestIDs {
			if inbox.RequestID == id {
				previousInbox[key] = inbox
				inbox.State = "completed"
				inbox.UpdatedAt = now
				s.state.Inbox[key] = inbox
				break
			}
		}
	}
	if err := s.saveLocked(); err != nil {
		for key, inbox := range previousInbox {
			s.state.Inbox[key] = inbox
		}
		if !duplicate {
			delete(s.state.Outbox, deliveryID)
		}
		return OutboxRecord{}, false, err
	}
	return record, duplicate, nil
}

func (s *Store) ClaimPending() (OutboxRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return OutboxRecord{}, false, ErrClosed
	}
	items := make([]OutboxRecord, 0)
	for _, record := range s.state.Outbox {
		if record.State == "pending" {
			items = append(items, record)
		}
	}
	if len(items) == 0 {
		return OutboxRecord{}, false, nil
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
	record := items[0]
	previous := record
	record.State = "sending"
	record.UpdatedAt = time.Now().UTC()
	s.state.Outbox[record.DeliveryID] = record
	if err := s.saveLocked(); err != nil {
		s.state.Outbox[record.DeliveryID] = previous
		return OutboxRecord{}, false, err
	}
	return record, true, nil
}

func (s *Store) SetOutboxState(deliveryID, state string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	record, ok := s.state.Outbox[deliveryID]
	if !ok {
		return ErrNotFound
	}
	if !ValidOutboxTransition(record.State, state) {
		return ErrConflict
	}
	previous := record
	record.State = state
	record.UpdatedAt = time.Now().UTC()
	if state == "sent" || state == "failed" {
		record.Text = ""
	}
	s.state.Outbox[deliveryID] = record
	if err := s.saveLocked(); err != nil {
		s.state.Outbox[deliveryID] = previous
		return err
	}
	return nil
}
