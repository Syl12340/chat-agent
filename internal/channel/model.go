// Package channel defines the transport-neutral boundary between external
// messaging platforms and the single chat-agent runtime.
package channel

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

const ContractVersion = 1

var ErrInvalidEnvelope = errors.New("invalid channel envelope")

type Conversation struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type Sender struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
}

type Message struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	ReplyTo string `json:"reply_to,omitempty"`
}

// Envelope contains only normalized fields needed by the runtime boundary.
// Raw provider payloads and credentials never belong in this structure.
type Envelope struct {
	Version       int          `json:"version"`
	EventID       string       `json:"event_id"`
	Platform      string       `json:"platform"`
	Adapter       string       `json:"adapter"`
	AccountID     string       `json:"account_id"`
	Conversation  Conversation `json:"conversation"`
	Sender        Sender       `json:"sender"`
	Message       Message      `json:"message"`
	OccurredAt    time.Time    `json:"occurred_at"`
	ReceivedAt    time.Time    `json:"received_at"`
	PayloadSHA256 string       `json:"payload_sha256"`
}

type Principal struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type ConversationKey struct {
	Platform         string `json:"platform"`
	AccountID        string `json:"account_id"`
	ConversationKind string `json:"conversation_kind"`
	ConversationID   string `json:"conversation_id"`
	RoleID           string `json:"role_id"`
}

type Mapping struct {
	Key       ConversationKey `json:"key"`
	SessionID string          `json:"session_id"`
	State     string          `json:"state"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type InboxRecord struct {
	Version       int       `json:"version"`
	EventID       string    `json:"event_id"`
	RequestID     string    `json:"request_id"`
	SessionID     string    `json:"session_id,omitempty"`
	PayloadSHA256 string    `json:"payload_sha256"`
	State         string    `json:"state"`
	ReceivedAt    time.Time `json:"received_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	Envelope      *Envelope `json:"envelope,omitempty"`
}

type OutboxRecord struct {
	Version       int       `json:"version"`
	DeliveryID    string    `json:"delivery_id"`
	SessionID     string    `json:"session_id"`
	RequestID     string    `json:"request_id"`
	TargetID      string    `json:"target_id"`
	ContentSHA256 string    `json:"content_sha256"`
	Text          string    `json:"text,omitempty"`
	State         string    `json:"state"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func MappingID(key ConversationKey) (string, error) {
	parts := []string{key.Platform, key.AccountID, key.ConversationKind, key.ConversationID, key.RoleID}
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
		if parts[i] == "" {
			return "", ErrInvalidEnvelope
		}
	}
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(digest[:]), nil
}

func PayloadDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

// RequestID is stable across webhook retries. Full SHA-256 avoids a second
// platform-specific identity namespace inside turns v3.
func RequestID(accountID, messageID string) (string, error) {
	accountID = strings.TrimSpace(accountID)
	messageID = strings.TrimSpace(messageID)
	if accountID == "" || messageID == "" {
		return "", ErrInvalidEnvelope
	}
	digest := sha256.Sum256([]byte("onebot:v11:" + accountID + ":" + messageID))
	return hex.EncodeToString(digest[:]), nil
}

func (e Envelope) Validate() error {
	if e.Version != ContractVersion || e.EventID == "" || e.Platform == "" || e.Adapter == "" || e.AccountID == "" || e.Conversation.Kind == "" || e.Conversation.ID == "" || e.Sender.ID == "" || e.Message.ID == "" || strings.TrimSpace(e.Message.Text) == "" || len(e.Message.Text) > 64<<10 || len(e.PayloadSHA256) != 64 || e.ReceivedAt.IsZero() {
		return ErrInvalidEnvelope
	}
	return nil
}

func ValidInboxTransition(from, to string) bool {
	switch from {
	case "":
		return to == "accepted"
	case "accepted":
		return to == "queued" || to == "failed" || to == "needs_attention"
	case "queued":
		return to == "completed" || to == "failed" || to == "needs_attention"
	default:
		return false
	}
}

func ValidOutboxTransition(from, to string) bool {
	switch from {
	case "":
		return to == "pending"
	case "pending":
		return to == "sending"
	case "sending":
		return to == "sent" || to == "unknown" || to == "failed"
	default:
		return false
	}
}
