// Package astrbot defines the narrow signed protocol used by an external
// AstrBot process. AstrBot remains a transport gateway; chat-agent owns the
// conversation, model, tools, memory and delivery settlement.
package astrbot

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"chat-agent/internal/channel"
)

const ProtocolVersion = 1

var ErrInvalidEvent = errors.New("invalid AstrBot bridge event")

// Event contains only portable platform facts. Raw AstrBot/platform payloads,
// access tokens and plugin configuration must never cross this boundary.
type Event struct {
	Version          int       `json:"version"`
	Platform         string    `json:"platform"`
	AccountID        string    `json:"account_id"`
	ConversationKind string    `json:"conversation_kind"`
	ConversationID   string    `json:"conversation_id"`
	SenderID         string    `json:"sender_id"`
	SenderName       string    `json:"sender_name,omitempty"`
	MessageID        string    `json:"message_id"`
	Text             string    `json:"text"`
	OccurredAt       time.Time `json:"occurred_at"`
}

func Decode(body []byte) (channel.Envelope, error) {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	var event Event
	var extra any
	if decoder.Decode(&event) != nil || decoder.Decode(&extra) != io.EOF || !valid(event) {
		return channel.Envelope{}, ErrInvalidEvent
	}
	requestID, err := channel.StableRequestID(event.Platform, "astrbot", event.AccountID, event.MessageID, event.ConversationID)
	if err != nil {
		return channel.Envelope{}, ErrInvalidEvent
	}
	envelope := channel.Envelope{
		Version:       channel.ContractVersion,
		EventID:       requestID,
		Platform:      event.Platform,
		Adapter:       "astrbot",
		AccountID:     event.AccountID,
		Conversation:  channel.Conversation{Kind: event.ConversationKind, ID: event.ConversationID},
		Sender:        channel.Sender{ID: event.SenderID, DisplayName: event.SenderName},
		Message:       channel.Message{ID: event.MessageID, Text: event.Text},
		OccurredAt:    event.OccurredAt.UTC(),
		ReceivedAt:    time.Now().UTC(),
		PayloadSHA256: channel.PayloadDigest(body),
	}
	if envelope.Validate() != nil {
		return channel.Envelope{}, ErrInvalidEvent
	}
	return envelope, nil
}

func valid(event Event) bool {
	return event.Version == ProtocolVersion && validPart(event.Platform, 64) && validPart(event.AccountID, 512) &&
		(event.ConversationKind == "private" || event.ConversationKind == "group") && validPart(event.ConversationID, 2048) &&
		validPart(event.SenderID, 512) && len(event.SenderName) <= 512 && validPart(event.MessageID, 512) &&
		strings.TrimSpace(event.Text) != "" && len(event.Text) <= 64<<10 && !event.OccurredAt.IsZero()
}

func validPart(value string, limit int) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= limit && !strings.ContainsAny(value, "\x00\r\n")
}

func Signature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func VerifySignature(secret string, body []byte, supplied string) bool {
	want := Signature(secret, body)
	return hmac.Equal([]byte(want), []byte(strings.TrimSpace(supplied)))
}

// VerifyToken compares the pull-channel credential without a timing-sensitive
// string comparison. The bridge is intended for loopback or private TLS links.
func VerifyToken(secret, supplied string) bool {
	want := sha256.Sum256([]byte(secret))
	got := sha256.Sum256([]byte(strings.TrimSpace(supplied)))
	return hmac.Equal(want[:], got[:])
}
