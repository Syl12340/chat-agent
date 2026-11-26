package api

import (
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"

	"chat-agent/internal/companion"
	"chat-agent/internal/interactive"
	"chat-agent/internal/probe"
)

const (
	continuationIdle = 30 * time.Minute
	continuationAge  = 24 * time.Hour
)

var resumeCue = regexp.MustCompile(`(?:下次|晚点|回头|改天|过会儿|等会儿|之后).{0,16}(?:再聊|继续|接着聊|再说|再讨论|聊聊)|(?:还没聊完|还没说完|下回接着)`)
var stopCue = regexp.MustCompile(`(?:别|不用|不必|不要|不想|不准).{0,12}(?:再聊|继续|接着聊|再提|再说)|别打扰|不用提醒`)
var errContinuationExpired = errors.New("continuation source is no longer current")

func continuationSubject(content string) string {
	if _, reserved, _ := interactive.ParseResponse(content); reserved {
		return ""
	}
	content = strings.Join(strings.FieldsFunc(content, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
	}), " ")
	if stopCue.MatchString(content) || !resumeCue.MatchString(content) {
		return ""
	}
	chars := []rune(content)
	return string(chars[:min(len(chars), 64)])
}

func (s *Server) chatContinuationSource(id string, sess *probe.Session, now time.Time) (string, string, error) {
	if s.publicChannelSession(id) {
		return "", "", companion.ErrCareBlocked
	}
	floor, err := s.uiContextFloor(sess, id)
	if err != nil {
		return "", "", err
	}
	messages := sess.Messages()
	index := len(messages) - 1
	for index >= floor && messages[index].Role != "user" {
		index--
	}
	if index < floor {
		return "", "", companion.ErrCareBlocked
	}
	message := messages[index]
	if !validID(message.ID) || message.Source != "" || message.Origin != "" {
		return "", "", companion.ErrCareBlocked
	}
	subject := continuationSubject(message.Content)
	if subject == "" {
		return "", "", companion.ErrCareBlocked
	}
	items, _, err := s.turns.List(id)
	if err != nil {
		return "", "", err
	}
	for i := len(items) - 1; i >= 0; i-- {
		turn := items[i]
		if turn.Status != "completed" || turn.StartIndex > index || index >= turn.EndIndex || turn.Content != message.Content {
			continue
		}
		// Merged rounds have several request IDs covering the same message range.
		// A match also has to belong to the private or application audience.
		if turn.Origin.ConversationKind != "group" && now.Sub(turn.CreatedAt) >= continuationIdle && now.Sub(turn.CreatedAt) <= continuationAge {
			return message.ID, subject, nil
		}
		break
	}
	return "", "", companion.ErrCareBlocked
}

func (s *Server) offerChatContinuation(role string, now time.Time) (companion.CareItem, error) {
	id, sess, err := s.proactiveSession(role, "")
	if errors.Is(err, errProactiveUnavailable) {
		return companion.CareItem{}, companion.ErrCareBlocked
	}
	if err != nil {
		return companion.CareItem{}, err
	}
	message, subject, err := s.chatContinuationSource(id, sess, now)
	if err != nil {
		return companion.CareItem{}, err
	}
	return s.life.OfferChatContinuation(role, id, message, subject, now, s.location)
}

func (s *Server) currentContinuation(item companion.CareItem, id string, session *probe.Session, now time.Time) (string, error) {
	if id != item.SourceSessionID || session.RoleID() != item.RoleID {
		return "", errContinuationExpired
	}
	pending, _, err := s.turns.HasQueued(id)
	if err != nil {
		return "", err
	}
	if pending {
		return "", errContinuationExpired
	}
	current, subject, err := s.chatContinuationSource(id, session, now)
	if err != nil && !errors.Is(err, companion.ErrCareBlocked) {
		return "", err
	}
	if current != item.SourceMessageID {
		return "", errContinuationExpired
	}
	return subject, nil
}
