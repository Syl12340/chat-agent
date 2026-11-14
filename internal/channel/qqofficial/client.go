// Package qqofficial implements the QQ Open Platform transport boundary. It
// owns OAuth, gateway resume and HTTP delivery only; CA owns sessions and Agent
// execution.
package qqofficial

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"chat-agent/internal/channel"
	"chat-agent/internal/storage"
	"chat-agent/internal/webtools"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const (
	apiBase      = "https://api.sgroup.qq.com"
	tokenURL     = "https://bots.qq.com/app/getAppAccessToken"
	groupIntent  = 1 << 25
	maxEventSize = 1 << 20
)

var (
	ErrRejected     = errors.New("qq official action rejected")
	ErrInvalidEvent = errors.New("invalid qq official event")
)

type Config struct {
	AppID         string
	Secret        string
	StateDir      string
	AllowGroups   bool
	AllowGroupAll bool
	HTTPClient    *http.Client
	Now           func() time.Time
}

type gatewayState struct {
	Version   int    `json:"version"`
	SessionID string `json:"session_id,omitempty"`
	Sequence  int64  `json:"sequence,omitempty"`
}

type tokenState struct {
	value   string
	expires time.Time
}

type Client struct {
	cfg   Config
	http  *http.Client
	mu    sync.Mutex
	token tokenState
	state gatewayState
}

func New(cfg Config) (*Client, error) {
	cfg.AppID = strings.TrimSpace(cfg.AppID)
	if cfg.AppID == "" || len(cfg.AppID) > 128 || len(cfg.Secret) < 16 || cfg.StateDir == "" {
		return nil, errors.New("QQ official AppID, secret and state directory are required")
	}
	if err := os.MkdirAll(cfg.StateDir, 0700); err != nil {
		return nil, err
	}
	c := &Client{cfg: cfg, http: cfg.HTTPClient, state: gatewayState{Version: 1}}
	if c.http == nil {
		c.http = webtools.NewPublicHTTPClient()
	}
	raw, err := os.ReadFile(filepath.Join(cfg.StateDir, "gateway.json"))
	if err == nil {
		if len(raw) > 4096 || json.Unmarshal(raw, &c.state) != nil || c.state.Version != 1 || c.state.Sequence < 0 || len(c.state.SessionID) > 256 {
			return nil, errors.New("invalid QQ gateway state")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return c, nil
}

func (c *Client) now() time.Time {
	if c.cfg.Now != nil {
		return c.cfg.Now().UTC()
	}
	return time.Now().UTC()
}

func (c *Client) Platform() string { return "qq-official" }

func (c *Client) Capabilities() channel.Capabilities {
	return channel.Capabilities{Text: true, Images: true, Files: true, Voice: true, Reply: true, Group: c.cfg.AllowGroups, Proactive: true, Delete: true}
}

func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	if c.token.value != "" && c.token.expires.After(c.now().Add(time.Minute)) {
		value := c.token.value
		c.mu.Unlock()
		return value, nil
	}
	c.mu.Unlock()
	body, _ := json.Marshal(map[string]string{"appId": c.cfg.AppID, "clientSecret": c.cfg.Secret})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", err
	}
	var result struct {
		AccessToken string          `json:"access_token"`
		ExpiresIn   json.RawMessage `json:"expires_in"`
		Code        int             `json:"code"`
	}
	if json.Unmarshal(raw, &result) != nil || resp.StatusCode != http.StatusOK || result.Code != 0 || result.AccessToken == "" {
		return "", fmt.Errorf("%w: token HTTP %d code %d", ErrRejected, resp.StatusCode, result.Code)
	}
	expires, err := rawInt(result.ExpiresIn)
	if err != nil || expires < 60 || expires > 24*60*60 {
		return "", errors.New("invalid QQ access token lifetime")
	}
	c.mu.Lock()
	c.token = tokenState{value: result.AccessToken, expires: c.now().Add(time.Duration(expires) * time.Second)}
	c.mu.Unlock()
	return result.AccessToken, nil
}

func rawInt(raw json.RawMessage) (int64, error) {
	var number json.Number
	if len(raw) > 0 && raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return 0, err
		}
		return strconv.ParseInt(text, 10, 64)
	}
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0, err
	}
	return strconv.ParseInt(number.String(), 10, 64)
}

func (c *Client) apiRequest(ctx context.Context, method, path string, body any, target any) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		raw, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			return marshalErr
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, apiBase+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "QQBot "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 128<<10))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: HTTP %d", ErrRejected, resp.StatusCode)
	}
	// QQ occasionally reports an application-level rejection with HTTP 200.
	// Treat it as a settled rejection so the outbox will not retry a message
	// whose provider outcome is already known.
	var status struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if len(raw) > 0 && json.Unmarshal(raw, &status) == nil && status.Code != 0 {
		return fmt.Errorf("%w: QQ %d %s", ErrRejected, status.Code, strings.TrimSpace(status.Message))
	}
	if target != nil && json.Unmarshal(raw, target) != nil {
		return errors.New("invalid QQ API response")
	}
	return nil
}

func (c *Client) gateway(ctx context.Context) (string, error) {
	var response struct {
		URL string `json:"url"`
	}
	if err := c.apiRequest(ctx, http.MethodGet, "/gateway", nil, &response); err != nil {
		return "", err
	}
	u, err := url.Parse(response.URL)
	if err != nil || u == nil {
		return "", errors.New("invalid QQ gateway URL")
	}
	host := strings.ToLower(u.Hostname())
	if u.Scheme != "wss" || host != "api.sgroup.qq.com" && host != "api.bot.qq.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid QQ gateway URL")
	}
	return u.String(), nil
}

type payload struct {
	ID string          `json:"id,omitempty"`
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
	S  int64           `json:"s,omitempty"`
	T  string          `json:"t,omitempty"`
}

func (c *Client) loadState() gatewayState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

func (c *Client) saveState(state gatewayState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := storage.Replace(filepath.Join(c.cfg.StateDir, "gateway.json"), raw); err != nil {
		return err
	}
	c.mu.Lock()
	c.state = state
	c.mu.Unlock()
	return nil
}

func (c *Client) clearSession() error {
	return c.saveState(gatewayState{Version: 1})
}

func (c *Client) Run(ctx context.Context, receive func(context.Context, channel.Envelope) error) error {
	if receive == nil {
		return errors.New("QQ receiver is required")
	}
	delay := time.Second
	for ctx.Err() == nil {
		err := c.runOnce(ctx, receive)
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if errors.Is(err, ErrRejected) {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return context.Cause(ctx)
		case <-timer.C:
		}
		if delay < 30*time.Second {
			delay *= 2
		}
	}
	return context.Cause(ctx)
}

func (c *Client) runOnce(ctx context.Context, receive func(context.Context, channel.Envelope) error) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	gateway, err := c.gateway(ctx)
	if err != nil {
		return err
	}
	conn, _, err := websocket.Dial(ctx, gateway, &websocket.DialOptions{HTTPClient: c.http, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(maxEventSize)
	var hello payload
	if err := wsjson.Read(ctx, conn, &hello); err != nil || hello.Op != 10 {
		return errors.New("QQ gateway did not send hello")
	}
	var helloData struct {
		HeartbeatInterval int `json:"heartbeat_interval"`
	}
	if json.Unmarshal(hello.D, &helloData) != nil || helloData.HeartbeatInterval < 1000 || helloData.HeartbeatInterval > 5*60*1000 {
		return errors.New("invalid QQ heartbeat interval")
	}
	state := c.loadState()
	if state.SessionID != "" {
		err = wsjson.Write(ctx, conn, map[string]any{"op": 6, "d": map[string]any{"token": "QQBot " + token, "session_id": state.SessionID, "seq": state.Sequence}})
	} else {
		err = wsjson.Write(ctx, conn, map[string]any{"op": 2, "d": map[string]any{"token": "QQBot " + token, "intents": groupIntent, "shard": []int{0, 1}, "properties": map[string]string{"$os": "linux", "$browser": "chat-agent", "$device": "chat-agent"}}})
	}
	if err != nil {
		return err
	}
	heartbeatCtx, heartbeatCancel := context.WithCancel(ctx)
	defer heartbeatCancel()
	heartbeatErr := make(chan error, 1)
	go c.heartbeat(heartbeatCtx, conn, time.Duration(helloData.HeartbeatInterval)*time.Millisecond, heartbeatErr)
	for {
		select {
		case err := <-heartbeatErr:
			return err
		default:
		}
		var event payload
		if err := wsjson.Read(ctx, conn, &event); err != nil {
			return err
		}
		switch event.Op {
		case 0:
			if event.T == "READY" {
				var ready struct {
					SessionID string `json:"session_id"`
				}
				if json.Unmarshal(event.D, &ready) != nil || ready.SessionID == "" {
					return errors.New("invalid QQ ready event")
				}
				state.SessionID = ready.SessionID
			}
			if event.T == "C2C_MESSAGE_CREATE" || event.T == "GROUP_AT_MESSAGE_CREATE" || event.T == "GROUP_MESSAGE_CREATE" {
				envelope, normalizeErr := c.normalize(event)
				if normalizeErr == nil {
					if err := receive(ctx, envelope); err != nil {
						return err
					}
				} else if !errors.Is(normalizeErr, ErrInvalidEvent) {
					return normalizeErr
				}
			}
			if event.S > 0 {
				state.Sequence = event.S
			}
			state.Version = 1
			if err := c.saveState(state); err != nil {
				return err
			}
		case 7:
			return errors.New("QQ gateway requested reconnect")
		case 9:
			_ = c.clearSession()
			return errors.New("QQ gateway session invalid")
		}
	}
}

func (c *Client) heartbeat(ctx context.Context, conn *websocket.Conn, interval time.Duration, result chan<- error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			state := c.loadState()
			if err := wsjson.Write(ctx, conn, map[string]any{"op": 1, "d": state.Sequence}); err != nil {
				select {
				case result <- err:
				default:
				}
				return
			}
		}
	}
}

type incomingMessage struct {
	ID          string `json:"id"`
	Content     string `json:"content"`
	Timestamp   string `json:"timestamp"`
	MessageType int    `json:"message_type"`
	GroupOpenID string `json:"group_openid"`
	Author      struct {
		ID           string `json:"id"`
		Username     string `json:"username"`
		UserOpenID   string `json:"user_openid"`
		MemberOpenID string `json:"member_openid"`
	} `json:"author"`
	MessageScene struct {
		Ext []string `json:"ext"`
	} `json:"message_scene"`
	Attachments []channel.Attachment `json:"attachments"`
}

func extValue(values []string, key string) string {
	prefix := key + "="
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			return strings.TrimPrefix(value, prefix)
		}
	}
	return ""
}

func (c *Client) normalize(event payload) (channel.Envelope, error) {
	var message incomingMessage
	if json.Unmarshal(event.D, &message) != nil || message.ID == "" {
		return channel.Envelope{}, ErrInvalidEvent
	}
	kind, conversationID, senderID := "private", message.Author.UserOpenID, message.Author.UserOpenID
	if event.T == "GROUP_AT_MESSAGE_CREATE" || event.T == "GROUP_MESSAGE_CREATE" {
		if !c.cfg.AllowGroups || event.T == "GROUP_MESSAGE_CREATE" && !c.cfg.AllowGroupAll {
			return channel.Envelope{}, ErrInvalidEvent
		}
		kind, conversationID, senderID = "group", message.GroupOpenID, message.Author.MemberOpenID
	}
	if conversationID == "" || senderID == "" {
		return channel.Envelope{}, ErrInvalidEvent
	}
	text := strings.TrimSpace(message.Content)
	if text == "" && len(message.Attachments) > 0 {
		parts := make([]string, 0, len(message.Attachments))
		for _, item := range message.Attachments {
			label := strings.TrimSpace(item.Filename)
			if label == "" {
				label = item.ContentType
			}
			parts = append(parts, "[附件："+label+"]")
		}
		text = strings.Join(parts, "\n")
	}
	if text == "" || len(text) > 64<<10 {
		return channel.Envelope{}, ErrInvalidEvent
	}
	discriminator := extValue(message.MessageScene.Ext, "msg_idx")
	requestID, err := channel.StableRequestID("qq-official", "gateway-v2", c.cfg.AppID, message.ID, discriminator)
	if err != nil {
		return channel.Envelope{}, err
	}
	occurred := c.now()
	if parsed, parseErr := time.Parse(time.RFC3339, message.Timestamp); parseErr == nil {
		occurred = parsed.UTC()
	}
	envelope := channel.Envelope{Version: channel.ContractVersion, EventID: requestID, Platform: "qq-official", Adapter: "gateway-v2", AccountID: c.cfg.AppID, Conversation: channel.Conversation{Kind: kind, ID: conversationID}, Sender: channel.Sender{ID: senderID, DisplayName: strings.TrimSpace(message.Author.Username)}, Message: channel.Message{ID: message.ID, Text: text, ReplyTo: extValue(message.MessageScene.Ext, "ref_msg_idx"), Attachments: message.Attachments}, OccurredAt: occurred, ReceivedAt: c.now(), PayloadSHA256: channel.PayloadDigest(event.D)}
	if err := envelope.Validate(); err != nil {
		return channel.Envelope{}, err
	}
	return envelope, nil
}

func (c *Client) Send(ctx context.Context, delivery channel.Delivery) (channel.DeliveryReceipt, error) {
	if strings.TrimSpace(delivery.TargetID) == "" || strings.TrimSpace(delivery.Text) == "" || len(delivery.Text) > 64<<10 {
		return channel.DeliveryReceipt{}, ErrInvalidEvent
	}
	path := "/v2/users/" + url.PathEscape(delivery.TargetID) + "/messages"
	if delivery.ConversationKind == "group" {
		path = "/v2/groups/" + url.PathEscape(delivery.TargetID) + "/messages"
	} else if delivery.ConversationKind != "private" {
		return channel.DeliveryReceipt{}, ErrInvalidEvent
	}
	body := map[string]any{"msg_type": 0, "content": delivery.Text, "msg_seq": 1}
	if delivery.ReplyTo != "" && !delivery.Proactive {
		body["msg_id"] = delivery.ReplyTo
	}
	if delivery.Proactive {
		body["is_wakeup"] = true
	}
	var response struct {
		ID string `json:"id"`
	}
	if err := c.apiRequest(ctx, http.MethodPost, path, body, &response); err != nil {
		return channel.DeliveryReceipt{}, err
	}
	if response.ID == "" {
		return channel.DeliveryReceipt{}, errors.New("QQ send response missing message ID")
	}
	return channel.DeliveryReceipt{ExternalMessageID: response.ID}, nil
}
