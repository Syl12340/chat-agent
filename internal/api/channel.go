package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	channelstore "chat-agent/internal/channel"
	"chat-agent/internal/channel/onebot"
	"chat-agent/internal/probe"
	"chat-agent/internal/turns"
)

type OneBotConfig struct {
	Enabled       bool
	AccountID     string
	OwnerID       string
	IngressSecret string
	ActionBaseURL string
	ActionToken   string
	RoleID        string
	Timeout       time.Duration
	MaxBodyBytes  int64
}

type multiCompletionObserver []TurnCompletionObserver

func (observers multiCompletionObserver) TurnCompleted(ctx context.Context, item TurnCompletion) error {
	for _, observer := range observers {
		if err := observer.TurnCompleted(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func combineCompletionObservers(configured TurnCompletionObserver, integrations ...TurnCompletionObserver) TurnCompletionObserver {
	observers := multiCompletionObserver{}
	for _, integration := range integrations {
		if integration != nil {
			observers = append(observers, integration)
		}
	}
	if configured != nil {
		observers = append(observers, configured)
	}
	if len(observers) == 0 {
		return nil
	}
	return observers
}

type oneBotIntegration struct {
	server *Server
	config OneBotConfig
	store  *channelstore.Store
	client *onebot.ActionClient
	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{}
	done   chan struct{}
	close  sync.Once
}

func newOneBotIntegration(server *Server, cfg OneBotConfig, dir string) (_ *oneBotIntegration, err error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if cfg.RoleID == "" {
		cfg.RoleID = "companion"
	}
	if cfg.ActionBaseURL == "" {
		cfg.ActionBaseURL = "http://127.0.0.1:13000/"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 256 << 10
	}
	if !decimalIdentity(cfg.AccountID) || !decimalIdentity(cfg.OwnerID) || cfg.AccountID == cfg.OwnerID || len(cfg.IngressSecret) < 32 || len(cfg.ActionToken) < 32 || cfg.MaxBodyBytes > 1<<20 {
		return nil, errors.New("invalid OneBot identity, credential or body limit")
	}
	if _, err := server.roles.Load(cfg.RoleID); err != nil {
		return nil, errors.New("invalid OneBot role")
	}
	client, err := onebot.NewActionClient(onebot.ActionConfig{BaseURL: cfg.ActionBaseURL, Token: cfg.ActionToken, Timeout: cfg.Timeout})
	if err != nil {
		return nil, err
	}
	store, err := channelstore.OpenStore(dir)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(server.workCtx)
	integration := &oneBotIntegration{server: server, config: cfg, store: store, client: client, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{})}
	defer func() {
		if err != nil {
			cancel()
			_ = store.Close()
		}
	}()
	if err = integration.reconcile(); err != nil {
		return nil, err
	}
	go integration.dispatch()
	integration.signal()
	return integration, nil
}

func decimalIdentity(value string) bool {
	n, err := strconv.ParseUint(value, 10, 64)
	return err == nil && n > 0
}

func (o *oneBotIntegration) signal() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

func (o *oneBotIntegration) TurnCompleted(_ context.Context, item TurnCompletion) error {
	mapping, ok := o.store.MappingForSession(item.SessionID)
	if !ok {
		return nil
	}
	originated := false
	for _, requestID := range item.RequestIDs {
		if _, ok := o.store.InboxForRequest(item.SessionID, requestID); ok {
			originated = true
			break
		}
	}
	if !originated {
		if _, ok := o.store.InboxForRequest(item.SessionID, item.RequestID); !ok {
			return nil
		}
	}
	text := channelstore.RenderText(item.Text)
	if text == "" {
		text = "[回复只包含当前消息渠道无法展示的内容，请在 chat-agent 网页中查看。]"
	}
	if _, _, err := o.store.SettleCompletion(item.SessionID, item.RequestID, mapping.Key.ConversationID, text, item.RequestIDs); err != nil {
		return err
	}
	o.signal()
	return nil
}

func (o *oneBotIntegration) reconcile() error {
	seen := map[string]bool{}
	for _, inbox := range o.store.QueuedInbox() {
		items, _, err := o.server.turns.List(inbox.SessionID)
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
			if err := o.store.SetInboxState(inbox.EventID, "needs_attention", inbox.SessionID); err != nil {
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
			groupKey := inbox.SessionID + ":" + root
			if seen[groupKey] {
				continue
			}
			seen[groupKey] = true
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
			if err := o.TurnCompleted(context.Background(), TurnCompletion{SessionID: inbox.SessionID, RequestID: root, RequestIDs: requestIDs, Text: text, CompletedAt: time.Now().UTC()}); err != nil {
				return err
			}
		case "failed", "canceled", "interrupted", "withdrawn":
			if err := o.store.FailRequests(inbox.SessionID, []string{inbox.RequestID}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (o *oneBotIntegration) dispatch() {
	defer close(o.done)
	for {
		select {
		case <-o.ctx.Done():
			return
		case <-o.wake:
		}
		for {
			record, ok, err := o.store.ClaimPending()
			if err != nil {
				log.Printf("OneBot outbox unavailable")
				break
			}
			if !ok {
				break
			}
			err = o.client.SendPrivateText(o.ctx, record.TargetID, record.Text)
			state := "sent"
			if err != nil {
				state = "unknown"
				if errors.Is(err, onebot.ErrActionRejected) || errors.Is(err, onebot.ErrInvalidEvent) {
					state = "failed"
				}
			}
			if settleErr := o.store.SetOutboxState(record.DeliveryID, state); settleErr != nil {
				log.Printf("OneBot outbox settlement failed")
				break
			}
		}
	}
}

func (o *oneBotIntegration) Close(ctx context.Context) error {
	o.close.Do(o.cancel)
	select {
	case <-o.done:
		return o.store.Close()
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (s *Server) oneBotWebhook(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.onebot == nil {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.onebot.config.MaxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil || int64(len(body)) > s.onebot.config.MaxBodyBytes {
		fail(w, http.StatusRequestEntityTooLarge, "OneBot 事件过大")
		return
	}
	if !onebot.VerifySignature(s.onebot.config.IngressSecret, body, r.Header.Get("X-Signature")) {
		fail(w, http.StatusUnauthorized, "OneBot 验签失败")
		return
	}
	envelope, err := onebot.Normalize(body, r.Header.Get("X-Self-ID"), onebot.WebhookConfig{AccountID: s.onebot.config.AccountID, OwnerID: s.onebot.config.OwnerID})
	if err != nil {
		if errors.Is(err, onebot.ErrIgnoredEvent) || errors.Is(err, onebot.ErrUnsupported) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		fail(w, http.StatusBadRequest, "OneBot 事件无效")
		return
	}
	inbox, duplicate, err := s.onebot.store.Accept(envelope, envelope.EventID)
	if err != nil {
		if errors.Is(err, channelstore.ErrConflict) {
			fail(w, http.StatusConflict, "OneBot 事件标识冲突")
		} else {
			fail(w, http.StatusServiceUnavailable, "OneBot Inbox 暂时不可用")
		}
		return
	}
	if duplicate && inbox.State != "accepted" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	mapping, sess, err := s.ensureOneBotSession(envelope)
	if err != nil {
		_ = s.onebot.store.SetInboxState(envelope.EventID, "needs_attention", "")
		fail(w, http.StatusServiceUnavailable, "OneBot 会话暂时不可用")
		return
	}
	if strings.TrimSpace(envelope.Message.Text) == "/continue" {
		if err := s.onebot.store.SetInboxState(envelope.EventID, "queued", mapping.SessionID); err != nil {
			fail(w, http.StatusServiceUnavailable, "OneBot Inbox 暂时不可用")
			return
		}
		if err := s.resumeOneBotSession(mapping.SessionID, sess); err != nil {
			_ = s.onebot.store.SetInboxState(envelope.EventID, "needs_attention", mapping.SessionID)
			fail(w, http.StatusConflict, "当前队列不能继续")
			return
		}
		if err := s.onebot.store.CompleteRequests(mapping.SessionID, []string{envelope.EventID}); err != nil {
			fail(w, http.StatusServiceUnavailable, "OneBot Inbox 暂时不可用")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	_, _, err = s.SubmitTurn(r.Context(), TurnSubmission{SessionID: mapping.SessionID, RequestID: envelope.EventID, Content: envelope.Message.Text, Mode: "auto", DeferStart: true})
	if err != nil {
		_ = s.onebot.store.SetInboxState(envelope.EventID, "needs_attention", mapping.SessionID)
		turnSubmissionFailure(w, err)
		return
	}
	if err := s.onebot.store.SetInboxState(envelope.EventID, "queued", mapping.SessionID); err != nil {
		fail(w, http.StatusServiceUnavailable, "OneBot Inbox 暂时不可用")
		return
	}
	if err := s.startSubmittedTurn(mapping.SessionID, sess); err != nil {
		_ = s.onebot.store.SetInboxState(envelope.EventID, "needs_attention", mapping.SessionID)
		fail(w, http.StatusServiceUnavailable, "OneBot 队列等待显式继续")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ensureOneBotSession(envelope channelstore.Envelope) (channelstore.Mapping, *probe.Session, error) {
	key := channelstore.ConversationKey{Platform: envelope.Platform, AccountID: envelope.AccountID, ConversationKind: envelope.Conversation.Kind, ConversationID: envelope.Conversation.ID, RoleID: s.onebot.config.RoleID}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return channelstore.Mapping{}, nil, err
	}
	mapping, _, err := s.onebot.store.ProvisionMapping(key, hex.EncodeToString(random[:]))
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
	sess, err := s.session(mapping.SessionID)
	if err != nil || sess.RoleID() != key.RoleID {
		return channelstore.Mapping{}, nil, errors.Join(errors.New("OneBot session role mismatch"), err)
	}
	if err := s.onebot.store.ActivateMapping(key, mapping.SessionID); err != nil {
		return channelstore.Mapping{}, nil, err
	}
	mapping.State = "active"
	return mapping, sess, nil
}

func (s *Server) resumeOneBotSession(sessionID string, sess *probe.Session) error {
	pending, _, err := s.turns.HasQueued(sessionID)
	if err != nil || !pending {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("server closing")
	}
	if s.runs[sessionID] != nil {
		return nil
	}
	if len(s.runs) >= 4 {
		return errors.New("model worker limit reached")
	}
	if err := s.turns.Pause(sessionID, false); err != nil {
		return err
	}
	s.startTurnWorkerLocked(sessionID, sess)
	return nil
}
