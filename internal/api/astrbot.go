package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	channelstore "chat-agent/internal/channel"
	astrbotwire "chat-agent/internal/channel/astrbot"
	"chat-agent/internal/probe"
	"chat-agent/internal/turns"
)

type AstrBotConfig struct {
	Enabled      bool
	Secret       string
	RoleID       string
	AllowGroups  bool
	MaxBodyBytes int64
	ClaimTimeout time.Duration
}

type astrBotIntegration struct {
	server *Server
	config AstrBotConfig
	store  *channelstore.Store
	mu     sync.Mutex
	timers map[string]*time.Timer
}

func newAstrBotIntegration(server *Server, cfg AstrBotConfig, dir string) (_ *astrBotIntegration, err error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if cfg.RoleID == "" {
		cfg.RoleID = "companion"
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 128 << 10
	}
	if cfg.ClaimTimeout <= 0 {
		cfg.ClaimTimeout = time.Minute
	}
	if len(cfg.Secret) < 32 || cfg.MaxBodyBytes > 1<<20 || cfg.ClaimTimeout < 10*time.Second || cfg.ClaimTimeout > 10*time.Minute {
		return nil, errors.New("invalid AstrBot bridge secret, body limit or claim timeout")
	}
	if _, err := server.roles.Load(cfg.RoleID); err != nil {
		return nil, errors.New("invalid AstrBot role")
	}
	store, err := channelstore.OpenStore(dir)
	if err != nil {
		return nil, err
	}
	integration := &astrBotIntegration{server: server, config: cfg, store: store, timers: map[string]*time.Timer{}}
	defer func() {
		if err != nil {
			_ = store.Close()
		}
	}()
	if err = integration.reconcile(); err != nil {
		return nil, err
	}
	return integration, nil
}

func (a *astrBotIntegration) TurnCompleted(_ context.Context, item TurnCompletion) error {
	mapping, ok := a.store.MappingForSession(item.SessionID)
	if !ok || item.Origin.Kind != "channel" || item.Origin.Platform != mapping.Key.Platform || item.Origin.AccountID != mapping.Key.AccountID || item.Origin.ConversationKind != mapping.Key.ConversationKind || item.Origin.ConversationID != mapping.Key.ConversationID {
		return nil
	}
	originated := false
	for _, requestID := range item.RequestIDs {
		if _, ok := a.store.InboxForRequest(item.SessionID, requestID); ok {
			originated = true
			break
		}
	}
	if !originated {
		return nil
	}
	text := channelstore.RenderText(item.Text)
	if text == "" {
		text = "[回复只包含当前消息渠道无法展示的内容，请在 chat-agent 网页中查看。]"
	}
	_, _, err := a.store.SettleDelivery(channelstore.DeliveryIntent{Platform: mapping.Key.Platform, AccountID: mapping.Key.AccountID, ConversationKind: mapping.Key.ConversationKind, SessionID: item.SessionID, RequestID: item.RequestID, TargetID: mapping.Key.ConversationID, Text: text}, item.RequestIDs)
	return err
}

func (a *astrBotIntegration) reconcile() error {
	seen := map[string]bool{}
	for _, inbox := range a.store.QueuedInbox() {
		items, _, err := a.server.turns.List(inbox.SessionID)
		if err != nil {
			return err
		}
		var current *turns.Turn
		for i := range items {
			if items[i].ID == inbox.RequestID {
				current = &items[i]
				break
			}
		}
		if current == nil {
			if err := a.store.SetInboxState(inbox.EventID, "needs_attention", inbox.SessionID); err != nil {
				return err
			}
			continue
		}
		switch current.Status {
		case "completed":
			root := current.BatchID
			if root == "" {
				root = current.ID
			}
			key := inbox.SessionID + ":" + root
			if seen[key] {
				continue
			}
			seen[key] = true
			requestIDs := []string{}
			text := ""
			for _, item := range items {
				if item.BatchID == root && item.Status == "completed" {
					requestIDs = append(requestIDs, item.ID)
				}
				if item.ID == root {
					text = item.Text
				}
			}
			if err := a.TurnCompleted(context.Background(), TurnCompletion{SessionID: inbox.SessionID, RequestID: root, RequestIDs: requestIDs, Text: text, Origin: current.Origin, CompletedAt: time.Now().UTC()}); err != nil {
				return err
			}
		case "failed", "canceled", "interrupted", "withdrawn":
			if err := a.store.FailRequests(inbox.SessionID, []string{inbox.RequestID}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Server) astrBotWebhook(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, s.astrbot.config.MaxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil || int64(len(body)) > s.astrbot.config.MaxBodyBytes {
		fail(w, http.StatusRequestEntityTooLarge, "AstrBot 事件过大")
		return
	}
	if !astrbotwire.VerifySignature(s.astrbot.config.Secret, body, r.Header.Get("X-Chat-Agent-Signature")) {
		fail(w, http.StatusUnauthorized, "AstrBot 网关验签失败")
		return
	}
	envelope, err := astrbotwire.Decode(body)
	if err != nil {
		fail(w, http.StatusBadRequest, "AstrBot 事件无效")
		return
	}
	if envelope.Conversation.Kind == "group" && !s.astrbot.config.AllowGroups {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if envelope.Conversation.Kind == "private" {
		identity, bound, bindErr := s.bindChannelIdentity(envelope)
		if _, isBind := linkCommand(envelope.Message.Text); isBind {
			reply := "绑定成功，以后可以通过 AstrBot 的这个账号继续和 chat-agent 聊天。"
			if bindErr != nil {
				reply = channelIdentityFailure(bindErr)
			}
			writeJSON(w, http.StatusOK, map[string]any{"accepted": bindErr == nil, "reply": reply})
			return
		}
		if bindErr != nil || !bound || identity.PrincipalID != ownerPrincipalID {
			writeJSON(w, http.StatusForbidden, map[string]any{"accepted": false, "reply": "请先在 chat-agent 的设置中生成绑定码，再发送：绑定 <绑定码>"})
			return
		}
	}
	inbox, duplicate, err := s.astrbot.store.Accept(envelope, envelope.EventID)
	if err != nil {
		if errors.Is(err, channelstore.ErrConflict) {
			fail(w, http.StatusConflict, "AstrBot 事件标识冲突")
		} else {
			fail(w, http.StatusServiceUnavailable, "AstrBot Inbox 暂时不可用")
		}
		return
	}
	if duplicate && inbox.State != "accepted" {
		writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "duplicate": true, "state": inbox.State, "event_id": envelope.EventID})
		return
	}
	mapping, sess, err := s.ensureAstrBotSession(envelope)
	if err != nil {
		_ = s.astrbot.store.SetInboxState(envelope.EventID, "needs_attention", "")
		fail(w, http.StatusServiceUnavailable, "AstrBot 会话暂时不可用")
		return
	}
	origin := turns.TurnOrigin{Kind: "channel", Platform: envelope.Platform, AccountID: envelope.AccountID, ConversationKind: envelope.Conversation.Kind, ConversationID: envelope.Conversation.ID, SenderID: envelope.Sender.ID}
	_, _, err = s.SubmitTurn(r.Context(), TurnSubmission{SessionID: mapping.SessionID, RequestID: envelope.EventID, Content: envelope.Message.Text, Mode: "auto", Origin: origin, DeferStart: true})
	if err != nil {
		_ = s.astrbot.store.SetInboxState(envelope.EventID, "needs_attention", mapping.SessionID)
		turnSubmissionFailure(w, err)
		return
	}
	if err := s.astrbot.store.SetInboxState(envelope.EventID, "queued", mapping.SessionID); err != nil {
		fail(w, http.StatusServiceUnavailable, "AstrBot Inbox 暂时不可用")
		return
	}
	if err := s.startSubmittedTurn(mapping.SessionID, sess); err != nil {
		_ = s.astrbot.store.SetInboxState(envelope.EventID, "needs_attention", mapping.SessionID)
		fail(w, http.StatusServiceUnavailable, "AstrBot 队列等待显式处理")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "duplicate": duplicate, "state": "queued", "event_id": envelope.EventID, "session_id": mapping.SessionID})
}

func (s *Server) ensureAstrBotSession(envelope channelstore.Envelope) (channelstore.Mapping, *probe.Session, error) {
	key := channelstore.ConversationKey{Platform: envelope.Platform, AccountID: envelope.AccountID, ConversationKind: envelope.Conversation.Kind, ConversationID: envelope.Conversation.ID, RoleID: s.astrbot.config.RoleID}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return channelstore.Mapping{}, nil, err
	}
	mapping, _, err := s.astrbot.store.ProvisionMapping(key, hex.EncodeToString(random[:]))
	if err != nil {
		return channelstore.Mapping{}, nil, err
	}
	_, historyErr := s.runtime.HistoryPath(mapping.SessionID)
	if errors.Is(historyErr, os.ErrNotExist) && mapping.State == "provisioning" {
		historyErr = s.runtime.Create(mapping.SessionID, key.RoleID)
	}
	if historyErr != nil {
		return channelstore.Mapping{}, nil, historyErr
	}
	if err := s.markChannelAudience(key, mapping.SessionID); err != nil {
		return channelstore.Mapping{}, nil, err
	}
	sess, err := s.session(mapping.SessionID)
	if err != nil || sess.RoleID() != key.RoleID {
		return channelstore.Mapping{}, nil, errors.Join(errors.New("AstrBot session role mismatch"), err)
	}
	if err := s.astrbot.store.ActivateMapping(key, mapping.SessionID); err != nil {
		return channelstore.Mapping{}, nil, err
	}
	mapping.State = "active"
	return mapping, sess, nil
}

func (s *Server) astrBotNextDelivery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !astrbotwire.VerifyToken(s.astrbot.config.Secret, r.Header.Get("X-Chat-Agent-Gateway-Token")) {
		fail(w, http.StatusUnauthorized, "AstrBot 网关认证失败")
		return
	}
	var record channelstore.OutboxRecord
	var err error
	for {
		var ok bool
		record, ok, err = s.astrbot.store.ClaimPending()
		if err != nil {
			fail(w, http.StatusServiceUnavailable, "AstrBot Outbox 暂时不可用")
			return
		}
		if !ok {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if record.Conversation != "private" || s.identities.HasAccount(record.Platform, record.AccountID) {
			break
		}
		// An identity may be revoked after the model settled a reply but before
		// the transport claimed it. Drop that reply instead of sending after
		// revocation, then continue to the next pending record.
		if err = s.astrbot.store.SetOutboxState(record.DeliveryID, "failed"); err != nil {
			fail(w, http.StatusServiceUnavailable, "AstrBot Outbox 暂时不可用")
			return
		}
	}
	s.astrbot.mu.Lock()
	s.astrbot.timers[record.DeliveryID] = time.AfterFunc(s.astrbot.config.ClaimTimeout, func() {
		_ = s.astrbot.store.SetOutboxState(record.DeliveryID, "unknown")
		s.astrbot.mu.Lock()
		delete(s.astrbot.timers, record.DeliveryID)
		s.astrbot.mu.Unlock()
	})
	s.astrbot.mu.Unlock()
	messages := channelstore.RenderBubbles(record.Text)
	if sess, sessionErr := s.session(record.SessionID); sessionErr == nil {
		if preferences, preferenceErr := s.companion.Get(sess.RoleID()); preferenceErr == nil && preferences.Preferences.ReplyRhythm == "single" {
			messages = []string{channelstore.RenderText(record.Text)}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"delivery_id": record.DeliveryID, "target": record.TargetID, "conversation_kind": record.Conversation, "messages": messages, "proactive": record.Proactive})
}

func (s *Server) astrBotAckDelivery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil || !astrbotwire.VerifySignature(s.astrbot.config.Secret, body, r.Header.Get("X-Chat-Agent-Signature")) {
		fail(w, http.StatusUnauthorized, "AstrBot 回执验签失败")
		return
	}
	var input struct {
		DeliveryID string `json:"delivery_id"`
		State      string `json:"state"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	var extra any
	id := r.PathValue("delivery")
	if decoder.Decode(&input) != nil || decoder.Decode(&extra) != io.EOF || input.DeliveryID != id || input.State != "sent" && input.State != "failed" && input.State != "unknown" {
		fail(w, http.StatusBadRequest, "AstrBot 回执无效")
		return
	}
	if err := s.astrbot.store.SetOutboxState(id, input.State); err != nil {
		if errors.Is(err, channelstore.ErrNotFound) {
			fail(w, http.StatusNotFound, "AstrBot delivery 不存在")
		} else {
			fail(w, http.StatusConflict, "AstrBot delivery 状态已变化")
		}
		return
	}
	s.astrbot.mu.Lock()
	if timer := s.astrbot.timers[id]; timer != nil {
		timer.Stop()
		delete(s.astrbot.timers, id)
	}
	s.astrbot.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *astrBotIntegration) Close() error {
	a.mu.Lock()
	for _, timer := range a.timers {
		timer.Stop()
	}
	a.timers = map[string]*time.Timer{}
	a.mu.Unlock()
	return a.store.Close()
}
