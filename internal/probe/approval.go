package probe

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// DefaultApprovalTTL bounds how long a confirmation stays usable. A ticket is
// only meaningful for the call it was issued for, so a long life buys nothing and
// ages into a risk.
const DefaultApprovalTTL = 10 * time.Minute

// deniedApprovalTTL bounds how long a refusal waits for a human. If nobody
// approves, the request disappears instead of leaving a permanent digest that
// some later call could match.
const deniedApprovalTTL = time.Hour

var (
	// ErrApprovalUnavailable means the host never wired an approval store, so no
	// required call can ever be authorized. That is an assembly bug, not a user
	// decision.
	ErrApprovalUnavailable = errors.New("宿主没有配置授权存储")
	// ErrApprovalUnknown means no refused call matches this confirmation. A model
	// cannot mint an authorization: there must first be a real, refused call.
	ErrApprovalUnknown = errors.New("没有与该调用匹配的待批准请求，可能已经过期或已经使用")
)

// ApprovalRequest describes one specific call waiting for a human.
//
// Digest is computed by the host from the canonical arguments. The model cannot
// talk another operation onto the same digest by reordering or respelling JSON.
type ApprovalRequest struct {
	Tool   string `json:"tool"`
	Digest string `json:"digest"`
	Reason string `json:"reason,omitempty"`
}

// ApprovalTicket is the host's record that a real user authorized exactly one
// call. It is bound to identity, role, session, tool and argument digest; any
// change to the arguments produces a different digest and the ticket no longer
// matches.
type ApprovalTicket struct {
	ID        string    `json:"id"`
	Tool      string    `json:"tool"`
	Digest    string    `json:"digest"`
	SessionID string    `json:"session_id"`
	RoleID    string    `json:"role_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ApprovalDigest hashes a tool name and its canonical arguments. canonical must
// be the same re-serialized form the executor validated, so two spellings of one
// payload cannot produce two identities.
func ApprovalDigest(tool string, canonical json.RawMessage) string {
	h := sha256.New()
	h.Write([]byte(tool))
	h.Write([]byte{0})
	h.Write(canonical)
	return hex.EncodeToString(h.Sum(nil))
}

type deniedApproval struct {
	request ApprovalRequest
	at      time.Time
}

// ApprovalStore keeps single-use authorizations in memory. Tickets never
// survive a restart: an authorization belongs to an interactive moment, and
// replaying one after a restart would be a guess about consent.
type ApprovalStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	tickets map[string]ApprovalTicket
	denied  map[string]deniedApproval
}

func NewApprovalStore() *ApprovalStore {
	return &ApprovalStore{
		ttl:     DefaultApprovalTTL,
		now:     time.Now,
		tickets: map[string]ApprovalTicket{},
		denied:  map[string]deniedApproval{},
	}
}

func approvalKey(sessionID, roleID, tool, digest string) string {
	return sessionID + "\x00" + roleID + "\x00" + tool + "\x00" + digest
}

// NoteDenied remembers that a call was refused for lack of authorization, so a
// later human confirmation can be matched to it. Without this record a
// confirmation would be an unbound "yes" to everything.
func (s *ApprovalStore) NoteDenied(sessionID, roleID, tool, digest, reason string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	s.denied[approvalKey(sessionID, roleID, tool, digest)] = deniedApproval{
		request: ApprovalRequest{Tool: tool, Digest: digest, Reason: reason},
		at:      s.now(),
	}
}

// Issue turns a human confirmation into a ticket. It only succeeds for a call
// the host itself recorded as refused; the caller cannot name an arbitrary
// digest.
func (s *ApprovalStore) Issue(sessionID, roleID, tool, digest string) (ApprovalTicket, error) {
	if s == nil {
		return ApprovalTicket{}, ErrApprovalUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	key := approvalKey(sessionID, roleID, tool, digest)
	if _, ok := s.denied[key]; !ok {
		return ApprovalTicket{}, ErrApprovalUnknown
	}
	delete(s.denied, key)
	id, err := newApprovalID()
	if err != nil {
		return ApprovalTicket{}, err
	}
	ticket := ApprovalTicket{
		ID:        id,
		Tool:      tool,
		Digest:    digest,
		SessionID: sessionID,
		RoleID:    roleID,
		ExpiresAt: s.now().Add(s.ttl),
	}
	s.tickets[key] = ticket
	return ticket, nil
}

// Consume takes the ticket for this exact call and removes it. Authorization is
// single-use by construction: a retry after success must ask again.
func (s *ApprovalStore) Consume(sessionID, roleID, tool, digest string) (ApprovalTicket, bool) {
	if s == nil {
		return ApprovalTicket{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	key := approvalKey(sessionID, roleID, tool, digest)
	ticket, ok := s.tickets[key]
	if !ok {
		return ApprovalTicket{}, false
	}
	delete(s.tickets, key)
	return ticket, true
}

func (s *ApprovalStore) pruneLocked() {
	now := s.now()
	for key, ticket := range s.tickets {
		if !ticket.ExpiresAt.After(now) {
			delete(s.tickets, key)
		}
	}
	for key, entry := range s.denied {
		if now.Sub(entry.at) > deniedApprovalTTL {
			delete(s.denied, key)
		}
	}
}

func newApprovalID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "apr_" + hex.EncodeToString(raw[:]), nil
}
