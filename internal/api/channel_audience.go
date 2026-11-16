package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	channelstore "chat-agent/internal/channel"
	"chat-agent/internal/storage"
)

const sessionAudienceVersion = 1

// sessionAudience is an immutable privacy floor for a conversation. A group
// transcript stays group-isolated even if its live channel mapping is moved or
// that integration is later disabled. Owner/private sessions need no sidecar.
type sessionAudience struct {
	Version        int       `json:"version"`
	SessionID      string    `json:"session_id"`
	Kind           string    `json:"kind"`
	Platform       string    `json:"platform"`
	AccountID      string    `json:"account_id"`
	ConversationID string    `json:"conversation_id"`
	CreatedAt      time.Time `json:"created_at"`
}

func (s *Server) audiencePath(sessionID string) string {
	return filepath.Join(s.config.DataDir, "session-audiences", sessionID+".json")
}

func validSessionAudience(item sessionAudience, sessionID string) bool {
	return item.Version == sessionAudienceVersion && item.SessionID == sessionID && validID(item.SessionID) &&
		item.Kind == "group-isolated" && item.Platform != "" && item.AccountID != "" && item.ConversationID != "" && !item.CreatedAt.IsZero()
}

func decodeSessionAudience(raw []byte, sessionID string) (sessionAudience, error) {
	if len(raw) > 16<<10 {
		return sessionAudience{}, errors.New("session audience exceeds capacity")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var item sessionAudience
	var extra any
	if decoder.Decode(&item) != nil || decoder.Decode(&extra) != io.EOF || !validSessionAudience(item, sessionID) {
		return sessionAudience{}, errors.New("damaged session audience")
	}
	return item, nil
}

func (s *Server) groupAudience(sessionID string) (sessionAudience, bool, error) {
	if !validID(sessionID) {
		return sessionAudience{}, false, errors.New("invalid session audience identity")
	}
	s.channelAudienceMu.Lock()
	defer s.channelAudienceMu.Unlock()
	raw, err := os.ReadFile(s.audiencePath(sessionID))
	if errors.Is(err, os.ErrNotExist) {
		return sessionAudience{}, false, nil
	}
	if err != nil {
		return sessionAudience{}, false, err
	}
	item, err := decodeSessionAudience(raw, sessionID)
	return item, err == nil, err
}

func (s *Server) markChannelAudience(key channelstore.ConversationKey, sessionID string) error {
	if key.ConversationKind != "group" {
		return nil
	}
	item := sessionAudience{Version: sessionAudienceVersion, SessionID: sessionID, Kind: "group-isolated", Platform: key.Platform, AccountID: key.AccountID, ConversationID: key.ConversationID, CreatedAt: time.Now().UTC()}
	if !validSessionAudience(item, sessionID) {
		return errors.New("invalid group audience")
	}
	s.channelAudienceMu.Lock()
	defer s.channelAudienceMu.Unlock()
	path := s.audiencePath(sessionID)
	if raw, err := os.ReadFile(path); err == nil {
		current, decodeErr := decodeSessionAudience(raw, sessionID)
		if decodeErr != nil {
			return decodeErr
		}
		if current.Platform != item.Platform || current.AccountID != item.AccountID || current.ConversationID != item.ConversationID {
			return errors.New("session already belongs to another group audience")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(item)
	if err != nil {
		return err
	}
	return storage.Replace(path, raw)
}

func (s *Server) backfillChannelAudiences() error {
	if s.onebot != nil {
		for _, mapping := range s.onebot.store.Mappings() {
			if err := s.markChannelAudience(mapping.Key, mapping.SessionID); err != nil {
				return err
			}
		}
	}
	if s.qqofficial != nil {
		for _, mapping := range s.qqofficial.store.Mappings() {
			if err := s.markChannelAudience(mapping.Key, mapping.SessionID); err != nil {
				return err
			}
		}
	}
	if s.astrbot != nil {
		for _, mapping := range s.astrbot.store.Mappings() {
			if err := s.markChannelAudience(mapping.Key, mapping.SessionID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Server) removeSessionAudience(sessionID string) error {
	s.channelAudienceMu.Lock()
	defer s.channelAudienceMu.Unlock()
	err := os.Remove(s.audiencePath(sessionID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
