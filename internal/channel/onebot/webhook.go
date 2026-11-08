// Package onebot implements the narrow OneBot v11 protocol boundary used by
// chat-agent. SnowLuma is one compatible external implementation; its runtime
// and source code are not embedded here.
package onebot

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"pocket-agent/internal/channel"
)

var (
	ErrInvalidSignature = errors.New("invalid onebot signature")
	ErrInvalidEvent     = errors.New("invalid onebot event")
	ErrIgnoredEvent     = errors.New("onebot event is outside the configured owner private channel")
	ErrUnsupported      = errors.New("unsupported onebot message")
)

type WebhookConfig struct {
	AccountID string
	OwnerID   string
	Adapter   string
	Now       func() time.Time
}

type id string

func (v *id) UnmarshalJSON(data []byte) error {
	var text string
	if len(data) > 0 && data[0] == '"' {
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
	} else {
		var number json.Number
		if err := json.Unmarshal(data, &number); err != nil {
			return err
		}
		text = number.String()
	}
	if text == "" {
		return ErrInvalidEvent
	}
	*v = id(text)
	return nil
}

type event struct {
	Time        int64           `json:"time"`
	SelfID      id              `json:"self_id"`
	PostType    string          `json:"post_type"`
	MessageType string          `json:"message_type"`
	SubType     string          `json:"sub_type"`
	MessageID   id              `json:"message_id"`
	UserID      id              `json:"user_id"`
	Message     json.RawMessage `json:"message"`
	Sender      struct {
		Nickname string `json:"nickname"`
		Card     string `json:"card"`
	} `json:"sender"`
}

// VerifySignature authenticates the exact bytes received on the wire. The
// sha1= prefix and HMAC behavior match the OneBot HTTP POST adapter contract.
func VerifySignature(secret string, body []byte, signature string) bool {
	if secret == "" || !strings.HasPrefix(signature, "sha1=") {
		return false
	}
	want, err := hex.DecodeString(strings.TrimPrefix(signature, "sha1="))
	if err != nil || len(want) != sha1.Size {
		return false
	}
	mac := hmac.New(sha1.New, []byte(secret))
	_, _ = mac.Write(body)
	return hmac.Equal(mac.Sum(nil), want)
}

func Signature(secret string, body []byte) string {
	mac := hmac.New(sha1.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha1=" + hex.EncodeToString(mac.Sum(nil))
}

func Normalize(body []byte, selfHeader string, cfg WebhookConfig) (channel.Envelope, error) {
	if len(body) == 0 || len(body) > 256<<10 || strings.TrimSpace(cfg.AccountID) == "" || strings.TrimSpace(cfg.OwnerID) == "" || selfHeader != cfg.AccountID {
		return channel.Envelope{}, ErrInvalidEvent
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var incoming event
	if err := decoder.Decode(&incoming); err != nil {
		return channel.Envelope{}, fmt.Errorf("%w: %v", ErrInvalidEvent, err)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return channel.Envelope{}, ErrInvalidEvent
	}
	accountID, ownerID := string(incoming.SelfID), string(incoming.UserID)
	if incoming.PostType != "message" || incoming.MessageType != "private" || incoming.SubType != "friend" || accountID != cfg.AccountID || ownerID != cfg.OwnerID || ownerID == accountID {
		return channel.Envelope{}, ErrIgnoredEvent
	}
	messageID := string(incoming.MessageID)
	if messageID == "" {
		return channel.Envelope{}, ErrInvalidEvent
	}
	text, err := plainText(incoming.Message)
	if err != nil {
		return channel.Envelope{}, err
	}
	requestID, err := channel.RequestID(accountID, messageID)
	if err != nil {
		return channel.Envelope{}, err
	}
	now := time.Now().UTC()
	if cfg.Now != nil {
		now = cfg.Now().UTC()
	}
	occurredAt := now
	if incoming.Time > 0 {
		occurredAt = time.Unix(incoming.Time, 0).UTC()
	}
	displayName := strings.TrimSpace(incoming.Sender.Card)
	if displayName == "" {
		displayName = strings.TrimSpace(incoming.Sender.Nickname)
	}
	adapter := strings.TrimSpace(cfg.Adapter)
	if adapter == "" {
		adapter = "onebot-http-post"
	}
	envelope := channel.Envelope{
		Version:       channel.ContractVersion,
		EventID:       requestID,
		Platform:      "onebot-v11",
		Adapter:       adapter,
		AccountID:     accountID,
		Conversation:  channel.Conversation{Kind: "private", ID: ownerID},
		Sender:        channel.Sender{ID: ownerID, DisplayName: displayName},
		Message:       channel.Message{ID: messageID, Text: text},
		OccurredAt:    occurredAt,
		ReceivedAt:    now,
		PayloadSHA256: channel.PayloadDigest(body),
	}
	if err := envelope.Validate(); err != nil {
		return channel.Envelope{}, err
	}
	return envelope, nil
}

func plainText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", ErrInvalidEvent
	}
	if raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) != nil || strings.Contains(text, "[CQ:") {
			return "", ErrUnsupported
		}
		text = strings.TrimSpace(text)
		if text == "" || len(text) > 64<<10 {
			return "", ErrUnsupported
		}
		return text, nil
	}
	var segments []struct {
		Type string `json:"type"`
		Data struct {
			Text string `json:"text"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &segments); err != nil || len(segments) == 0 {
		return "", ErrUnsupported
	}
	var out strings.Builder
	for _, segment := range segments {
		if segment.Type != "text" {
			return "", ErrUnsupported
		}
		if out.Len()+len(segment.Data.Text) > 64<<10 {
			return "", ErrUnsupported
		}
		out.WriteString(segment.Data.Text)
	}
	text := strings.TrimSpace(out.String())
	if text == "" {
		return "", ErrUnsupported
	}
	return text, nil
}

func decimalID(value string) (int64, error) {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n <= 0 {
		return 0, ErrInvalidEvent
	}
	return n, nil
}
