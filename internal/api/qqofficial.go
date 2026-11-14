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
	"strings"
	"sync"
	"time"

	channelstore "chat-agent/internal/channel"
	"chat-agent/internal/channel/qqofficial"
	"chat-agent/internal/media"
	"chat-agent/internal/probe"
	"chat-agent/internal/turns"
	"chat-agent/internal/webtools"
)

type QQOfficialConfig struct {
	Enabled       bool
	AppID         string
	Secret        string
	RoleID        string
	AllowGroups   bool
	AllowGroupAll bool
}

type qqOfficialIntegration struct {
	server  *Server
	config  QQOfficialConfig
	store   *channelstore.Store
	adapter *qqofficial.Client
	http    *http.Client
	ctx     context.Context
	cancel  context.CancelFunc
	wake    chan struct{}
	done    chan struct{}
	close   sync.Once
}

func newQQOfficialIntegration(server *Server, cfg QQOfficialConfig, dir string) (_ *qqOfficialIntegration, err error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if cfg.RoleID == "" {
		cfg.RoleID = "companion"
	}
	if strings.TrimSpace(cfg.AppID) == "" || len(cfg.Secret) < 16 {
		return nil, errors.New("invalid QQ official credentials")
	}
	if _, err := server.roles.Load(cfg.RoleID); err != nil {
		return nil, errors.New("invalid QQ official role")
	}
	store, err := channelstore.OpenStore(dir)
	if err != nil {
		return nil, err
	}
	adapter, err := qqofficial.New(qqofficial.Config{AppID: cfg.AppID, Secret: cfg.Secret, StateDir: dir, AllowGroups: cfg.AllowGroups, AllowGroupAll: cfg.AllowGroupAll})
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(server.workCtx)
	integration := &qqOfficialIntegration{server: server, config: cfg, store: store, adapter: adapter, http: webtools.NewPublicHTTPClient(), ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{})}
	defer func() {
		if err != nil {
			cancel()
			_ = store.Close()
		}
	}()
	if err = integration.reconcile(); err != nil {
		return nil, err
	}
	go integration.run()
	integration.signal()
	return integration, nil
}

func (q *qqOfficialIntegration) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *qqOfficialIntegration) run() {
	defer close(q.done)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := q.adapter.Run(q.ctx, q.receive); err != nil && q.ctx.Err() == nil {
			log.Printf("QQ official gateway stopped: %v", err)
		}
	}()
	q.dispatch()
	wg.Wait()
}

func (q *qqOfficialIntegration) receive(ctx context.Context, envelope channelstore.Envelope) error {
	if envelope.Conversation.Kind == "private" {
		identity, bound, bindErr := q.server.bindChannelIdentity(envelope)
		if _, isBind := linkCommand(envelope.Message.Text); isBind {
			text := "绑定成功，以后可以直接在 QQ 和 chat-agent 之间继续聊天。"
			if bindErr != nil {
				text = channelIdentityFailure(bindErr)
			}
			_, sendErr := q.adapter.Send(ctx, channelstore.Delivery{AccountID: envelope.AccountID, ConversationKind: "private", TargetID: envelope.Conversation.ID, ReplyTo: envelope.Message.ID, Text: text})
			return sendErr
		}
		if bindErr != nil || !bound || identity.PrincipalID != ownerPrincipalID {
			_, sendErr := q.adapter.Send(ctx, channelstore.Delivery{AccountID: envelope.AccountID, ConversationKind: "private", TargetID: envelope.Conversation.ID, ReplyTo: envelope.Message.ID, Text: "请先在 chat-agent 的设置中生成绑定码，再发送：绑定 <绑定码>"})
			return sendErr
		}
	}
	inbox, duplicate, err := q.store.Accept(envelope, envelope.EventID)
	if err != nil {
		return err
	}
	if duplicate && inbox.State != "accepted" {
		return nil
	}
	mapping, sess, err := q.ensureSession(envelope)
	if err != nil {
		_ = q.store.SetInboxState(envelope.EventID, "needs_attention", "")
		return err
	}
	images := q.downloadImages(ctx, sess.RoleID(), mapping.SessionID, envelope.Message.Attachments)
	_, _, err = q.server.SubmitTurn(ctx, TurnSubmission{SessionID: mapping.SessionID, RequestID: envelope.EventID, Content: envelope.Message.Text, Mode: "auto", Images: images, DeferStart: true})
	if err != nil {
		_ = q.store.SetInboxState(envelope.EventID, "needs_attention", mapping.SessionID)
		return err
	}
	if err := q.store.SetInboxState(envelope.EventID, "queued", mapping.SessionID); err != nil {
		return err
	}
	if err := q.server.startSubmittedTurn(mapping.SessionID, sess); err != nil {
		_ = q.store.SetInboxState(envelope.EventID, "needs_attention", mapping.SessionID)
		return err
	}
	return nil
}

func (q *qqOfficialIntegration) downloadImages(ctx context.Context, roleID, sessionID string, attachments []channelstore.Attachment) []media.Image {
	choice, err := q.server.selectChat("", "")
	if err != nil || !choice.Vision {
		return nil
	}
	items := make([]media.Image, 0, min(len(attachments), 3))
	for _, attachment := range attachments {
		if len(items) >= 3 || attachment.ContentType != "image/jpeg" && attachment.ContentType != "image/png" {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, attachment.URL, nil)
		if err != nil {
			continue
		}
		resp, err := q.http.Do(req)
		if err != nil {
			continue
		}
		if resp.StatusCode != http.StatusOK || resp.ContentLength > media.MaxImageBytes {
			_ = resp.Body.Close()
			continue
		}
		q.server.mediaMu.Lock()
		item, uploadErr := q.server.images.Upload(roleID, sessionID, io.LimitReader(resp.Body, media.MaxImageBytes+1))
		q.server.mediaMu.Unlock()
		_ = resp.Body.Close()
		if uploadErr == nil {
			items = append(items, item)
		}
	}
	return items
}

func (q *qqOfficialIntegration) ensureSession(envelope channelstore.Envelope) (channelstore.Mapping, *probe.Session, error) {
	key := channelstore.ConversationKey{Platform: envelope.Platform, AccountID: envelope.AccountID, ConversationKind: envelope.Conversation.Kind, ConversationID: envelope.Conversation.ID, RoleID: q.config.RoleID}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return channelstore.Mapping{}, nil, err
	}
	mapping, _, err := q.store.ProvisionMapping(key, hex.EncodeToString(random[:]))
	if err != nil {
		return channelstore.Mapping{}, nil, err
	}
	_, historyErr := q.server.runtime.HistoryPath(mapping.SessionID)
	if errors.Is(historyErr, os.ErrNotExist) && mapping.State == "provisioning" {
		historyErr = q.server.runtime.Create(mapping.SessionID, key.RoleID)
	}
	if historyErr != nil {
		return channelstore.Mapping{}, nil, historyErr
	}
	sess, err := q.server.session(mapping.SessionID)
	if err != nil || sess.RoleID() != key.RoleID {
		return channelstore.Mapping{}, nil, errors.Join(errors.New("QQ official session role mismatch"), err)
	}
	if err := q.store.ActivateMapping(key, mapping.SessionID); err != nil {
		return channelstore.Mapping{}, nil, err
	}
	mapping.State = "active"
	return mapping, sess, nil
}

func (q *qqOfficialIntegration) TurnCompleted(_ context.Context, item TurnCompletion) error {
	mapping, ok := q.store.MappingForSession(item.SessionID)
	if !ok {
		return nil
	}
	var origin channelstore.InboxRecord
	for _, requestID := range append(append([]string{}, item.RequestIDs...), item.RequestID) {
		if candidate, found := q.store.InboxForRequest(item.SessionID, requestID); found {
			origin, ok = candidate, true
			break
		}
	}
	if !ok {
		return nil
	}
	text := channelstore.RenderText(item.Text)
	if text == "" {
		text = "回复包含只能在 chat-agent 应用内显示的内容，请在应用中查看。"
	}
	intent := channelstore.DeliveryIntent{Platform: mapping.Key.Platform, AccountID: mapping.Key.AccountID, ConversationKind: mapping.Key.ConversationKind, SessionID: item.SessionID, RequestID: item.RequestID, TargetID: mapping.Key.ConversationID, ReplyTo: origin.ExternalID, Text: text}
	if _, _, err := q.store.SettleDelivery(intent, item.RequestIDs); err != nil {
		return err
	}
	q.signal()
	return nil
}

func (q *qqOfficialIntegration) reconcile() error {
	seen := map[string]bool{}
	for _, inbox := range q.store.QueuedInbox() {
		items, _, err := q.server.turns.List(inbox.SessionID)
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
			if err := q.store.SetInboxState(inbox.EventID, "needs_attention", inbox.SessionID); err != nil {
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
			requestIDs, text := []string{}, ""
			for _, turn := range items {
				if turn.BatchID == root && turn.Status == "completed" {
					requestIDs = append(requestIDs, turn.ID)
				}
				if turn.ID == root {
					text = turn.Text
				}
			}
			if err := q.TurnCompleted(context.Background(), TurnCompletion{SessionID: inbox.SessionID, RequestID: root, RequestIDs: requestIDs, Text: text, CompletedAt: time.Now().UTC()}); err != nil {
				return err
			}
		case "failed", "canceled", "interrupted", "withdrawn":
			if err := q.store.FailRequests(inbox.SessionID, []string{inbox.RequestID}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (q *qqOfficialIntegration) dispatch() {
	for {
		select {
		case <-q.ctx.Done():
			return
		case <-q.wake:
		}
		for {
			record, ok, err := q.store.ClaimPending()
			if err != nil || !ok {
				break
			}
			_, err = q.adapter.Send(q.ctx, channelstore.Delivery{ID: record.DeliveryID, AccountID: record.AccountID, ConversationKind: record.Conversation, TargetID: record.TargetID, ReplyTo: record.ReplyTo, Text: record.Text, Proactive: record.Proactive})
			state := "sent"
			if err != nil {
				state = "unknown"
				if errors.Is(err, qqofficial.ErrRejected) || errors.Is(err, qqofficial.ErrInvalidEvent) {
					state = "failed"
				}
			}
			if settleErr := q.store.SetOutboxState(record.DeliveryID, state); settleErr != nil {
				log.Printf("QQ official outbox settlement failed")
				break
			}
		}
	}
}

func (q *qqOfficialIntegration) Close(ctx context.Context) error {
	q.close.Do(q.cancel)
	select {
	case <-q.done:
		return q.store.Close()
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}
