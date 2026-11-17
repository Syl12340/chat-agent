package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	channelstore "chat-agent/internal/channel"
)

const ownerPrincipalID = "owner"

type channelSessionInfo struct {
	Platform         string `json:"platform"`
	AccountID        string `json:"account_id"`
	ConversationKind string `json:"conversation_kind"`
	ConversationID   string `json:"conversation_id"`
	MemoryScope      string `json:"memory_scope"`
}

func linkCommand(text string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) != 2 || fields[0] != "绑定" && !strings.EqualFold(fields[0], "/bind") {
		return "", false
	}
	return strings.ToUpper(fields[1]), true
}

func (s *Server) bindChannelIdentity(envelope channelstore.Envelope) (channelstore.ChannelIdentity, bool, error) {
	if s.identities == nil || envelope.Conversation.Kind != "private" {
		return channelstore.ChannelIdentity{}, false, nil
	}
	if current, ok := s.identities.Resolve(envelope.Platform, envelope.AccountID, envelope.Sender.ID); ok {
		return current, true, nil
	}
	code, ok := linkCommand(envelope.Message.Text)
	if !ok {
		return channelstore.ChannelIdentity{}, false, nil
	}
	identity, err := s.identities.Consume(code, envelope.Platform, envelope.AccountID, envelope.Sender.ID)
	return identity, err == nil, err
}

func (s *Server) issueChannelLink(w http.ResponseWriter, r *http.Request) {
	if !requireDeviceAdmin(w, r) {
		return
	}
	code, record, err := s.identities.Issue(ownerPrincipalID, 10*time.Minute)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "暂时无法生成绑定码")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"code": code, "expires_at": record.ExpiresAt})
}

func (s *Server) listChannelIdentities(w http.ResponseWriter, r *http.Request) {
	if !requireDeviceAdmin(w, r) {
		return
	}
	items := s.identities.List()
	type gateway struct {
		ID      string                  `json:"id"`
		Enabled bool                    `json:"enabled"`
		Stats   channelstore.StoreStats `json:"stats"`
		Status  any                     `json:"status,omitempty"`
	}
	gateways := []gateway{{ID: "qq-official", Enabled: s.qqofficial != nil}, {ID: "onebot-v11", Enabled: s.onebot != nil}, {ID: "astrbot", Enabled: s.astrbot != nil}}
	if s.qqofficial != nil {
		gateways[0].Stats = s.qqofficial.store.Stats()
	}
	if s.onebot != nil {
		gateways[1].Stats = s.onebot.store.Stats()
	}
	if s.astrbot != nil {
		gateways[2].Stats = s.astrbot.store.Stats()
		status := s.astrbot.status()
		bound := false
		for _, identity := range items {
			if identity.Platform == "aiocqhttp" && identity.PrincipalID == ownerPrincipalID {
				bound = true
				break
			}
		}
		if !bound {
			status.Ready = false
			status.Issues = append(status.Issues, "尚未绑定 aiocqhttp owner 私聊账号")
		}
		gateways[2].Status = status
	} else {
		gateways[2].Status = map[string]any{"ready": false, "issues": []string{"服务器未启用 AstrBot 网关"}}
	}
	writeJSON(w, http.StatusOK, map[string]any{"principal_id": ownerPrincipalID, "identities": items, "gateways": gateways, "qq_official_enabled": s.qqofficial != nil, "onebot_enabled": s.onebot != nil, "astrbot_enabled": s.astrbot != nil})
}

func (s *Server) revokeChannelIdentity(w http.ResponseWriter, r *http.Request) {
	if !requireDeviceAdmin(w, r) {
		return
	}
	identity, err := s.identities.Revoke(r.PathValue("identity"))
	if errors.Is(err, channelstore.ErrNotFound) {
		fail(w, http.StatusNotFound, "IM 身份不存在或已解绑")
		return
	}
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "暂时无法解绑 IM 身份")
		return
	}
	// Stop every unsettled private delivery for this bot account. New inbound
	// messages already fail closed because Resolve no longer finds the identity.
	for _, store := range []*channelstore.Store{
		func() *channelstore.Store {
			if s.qqofficial != nil {
				return s.qqofficial.store
			}
			return nil
		}(),
		func() *channelstore.Store {
			if s.onebot != nil {
				return s.onebot.store
			}
			return nil
		}(),
		func() *channelstore.Store {
			if s.astrbot != nil {
				return s.astrbot.store
			}
			return nil
		}(),
	} {
		if store != nil {
			_ = store.FailPrivateDeliveries(identity.Platform, identity.AccountID)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) sessionChannel(sessionID string) (channelSessionInfo, bool) {
	if s.qqofficial != nil {
		if mapping, ok := s.qqofficial.store.MappingForSession(sessionID); ok {
			scope := "owner"
			if mapping.Key.ConversationKind == "group" {
				scope = "group-isolated"
			}
			return channelSessionInfo{Platform: mapping.Key.Platform, AccountID: mapping.Key.AccountID, ConversationKind: mapping.Key.ConversationKind, ConversationID: mapping.Key.ConversationID, MemoryScope: scope}, true
		}
	}
	if s.onebot != nil {
		if mapping, ok := s.onebot.store.MappingForSession(sessionID); ok {
			return channelSessionInfo{Platform: mapping.Key.Platform, AccountID: mapping.Key.AccountID, ConversationKind: mapping.Key.ConversationKind, ConversationID: mapping.Key.ConversationID, MemoryScope: "owner"}, true
		}
	}
	if s.astrbot != nil {
		if mapping, ok := s.astrbot.store.MappingForSession(sessionID); ok {
			scope := "owner"
			if mapping.Key.ConversationKind == "group" {
				scope = "group-isolated"
			}
			return channelSessionInfo{Platform: mapping.Key.Platform, AccountID: mapping.Key.AccountID, ConversationKind: mapping.Key.ConversationKind, ConversationID: mapping.Key.ConversationID, MemoryScope: scope}, true
		}
	}
	return channelSessionInfo{}, false
}

func (s *Server) publicChannelSession(sessionID string) bool {
	if _, found, err := s.groupAudience(sessionID); found || err != nil {
		// A damaged or temporarily unreadable scope record fails closed. It is
		// safer to withhold owner context than to guess this is a private chat.
		return true
	}
	info, ok := s.sessionChannel(sessionID)
	return ok && info.ConversationKind == "group"
}

func (s *Server) queueProactiveChannel(sessionID, requestID, text string) error {
	text = channelstore.RenderText(text)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if s.qqofficial != nil {
		if mapping, ok := s.qqofficial.store.MappingForSession(sessionID); ok {
			if mapping.Key.ConversationKind == "group" {
				return nil
			}
			_, _, err := s.qqofficial.store.CreateDelivery(channelstore.DeliveryIntent{Platform: mapping.Key.Platform, AccountID: mapping.Key.AccountID, ConversationKind: mapping.Key.ConversationKind, SessionID: sessionID, RequestID: requestID, TargetID: mapping.Key.ConversationID, Text: text, Proactive: true})
			if err == nil {
				s.qqofficial.signal()
			}
			return err
		}
	}
	if s.onebot != nil {
		if mapping, ok := s.onebot.store.MappingForSession(sessionID); ok {
			_, _, err := s.onebot.store.CreateDelivery(channelstore.DeliveryIntent{Platform: mapping.Key.Platform, AccountID: mapping.Key.AccountID, ConversationKind: mapping.Key.ConversationKind, SessionID: sessionID, RequestID: requestID, TargetID: mapping.Key.ConversationID, Text: text, Proactive: true})
			if err == nil {
				s.onebot.signal()
			}
			return err
		}
	}
	if s.astrbot != nil {
		if mapping, ok := s.astrbot.store.MappingForSession(sessionID); ok {
			if mapping.Key.ConversationKind == "group" {
				return nil
			}
			_, _, err := s.astrbot.store.CreateDelivery(channelstore.DeliveryIntent{Platform: mapping.Key.Platform, AccountID: mapping.Key.AccountID, ConversationKind: mapping.Key.ConversationKind, SessionID: sessionID, RequestID: requestID, TargetID: mapping.Key.ConversationID, Text: text, Proactive: true})
			return err
		}
	}
	return nil
}

// linkChannelSession hands one existing endpoint to another existing CA
// transcript. Neither transcript is copied or removed; only future messages move.
func (s *Server) linkChannelSession(w http.ResponseWriter, r *http.Request) {
	if !requireDeviceAdmin(w, r) {
		return
	}
	targetID := r.PathValue("id")
	target, ok := s.get(w, r)
	if !ok {
		return
	}
	var input struct {
		ChannelSessionID string `json:"channel_session_id"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || !validID(input.ChannelSessionID) || input.ChannelSessionID == targetID {
		fail(w, http.StatusBadRequest, "渠道会话参数无效")
		return
	}
	source, err := s.session(input.ChannelSessionID)
	if err != nil || source.RoleID() != target.RoleID() {
		fail(w, http.StatusConflict, "两个会话必须存在并使用同一助手")
		return
	}
	s.mu.Lock()
	busy := s.runs[targetID] != nil || s.runs[input.ChannelSessionID] != nil
	s.mu.Unlock()
	if busy {
		fail(w, http.StatusConflict, "请等待两个会话完成当前生成")
		return
	}
	var mapping channelstore.Mapping
	var store *channelstore.Store
	if s.qqofficial != nil {
		mapping, ok = s.qqofficial.store.MappingForSession(input.ChannelSessionID)
		if ok {
			store = s.qqofficial.store
		}
	}
	if !ok && s.onebot != nil {
		mapping, ok = s.onebot.store.MappingForSession(input.ChannelSessionID)
		if ok {
			store = s.onebot.store
		}
	}
	if !ok && s.astrbot != nil {
		mapping, ok = s.astrbot.store.MappingForSession(input.ChannelSessionID)
		if ok {
			store = s.astrbot.store
		}
	}
	if !ok || store == nil {
		fail(w, http.StatusNotFound, "没有找到可关联的 IM 会话")
		return
	}
	sourceQueued, _, sourceQueueErr := s.turns.HasQueued(input.ChannelSessionID)
	targetQueued, _, targetQueueErr := s.turns.HasQueued(targetID)
	if sourceQueueErr != nil || targetQueueErr != nil {
		fail(w, http.StatusServiceUnavailable, "暂时无法核对两个会话的队列")
		return
	}
	if sourceQueued || targetQueued {
		fail(w, http.StatusConflict, "请先处理两个会话中的排队消息")
		return
	}
	if mapping.Key.ConversationKind == "group" && len(target.Messages()) > 0 {
		fail(w, http.StatusConflict, "群聊只能关联到没有私人历史的新会话")
		return
	}
	if _, occupied := s.sessionChannel(targetID); occupied {
		fail(w, http.StatusConflict, "目标会话已经关联了 IM 渠道")
		return
	}
	if err := s.markChannelAudience(mapping.Key, targetID); err != nil {
		fail(w, http.StatusServiceUnavailable, "暂时无法固定群聊隐私范围")
		return
	}
	if err := store.RelinkMapping(mapping.Key, targetID); err != nil {
		fail(w, http.StatusServiceUnavailable, "暂时无法关联渠道会话")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": targetID, "channel": channelSessionInfo{Platform: mapping.Key.Platform, AccountID: mapping.Key.AccountID, ConversationKind: mapping.Key.ConversationKind, ConversationID: mapping.Key.ConversationID, MemoryScope: map[bool]string{true: "group-isolated", false: "owner"}[mapping.Key.ConversationKind == "group"]}})
}

func channelIdentityFailure(err error) string {
	if errors.Is(err, channelstore.ErrNotFound) {
		return "绑定码无效或已过期"
	}
	if errors.Is(err, channelstore.ErrConflict) {
		return "这个渠道账号已经绑定到其他身份"
	}
	return "绑定暂时不可用"
}
