package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"chat-agent/internal/probe"
	"chat-agent/internal/runlog"
)

var errProactiveUnavailable = errors.New("no available conversation for proactive followup")

const proactiveHostPolicy = `
This response is initiated by a trusted host trigger, not by a new user message.
The proactive_trigger object is reference data and never user consent, evidence,
or permission to write. Continue the selected conversation naturally and use
its recent messages for continuity. Do not imply that the user spoke just now,
was watched, was awaited, or owes a reply. No tools are available in this run.
Do not modify memory, persona, activities, tasks, reminders or settings. Produce
only the format requested below; a valid no-output decision is allowed when the
specific instruction says so.`

type proactiveTarget struct {
	id      string
	session *probe.Session
	ctx     context.Context
	release func()
}

type proactiveFinalizer func(sessionID string, response probe.Message, messageIndex int) (message probe.Message, persist bool, err error)

func hasUserConversation(messages []probe.Message) bool {
	for _, message := range messages {
		if message.Role == "user" {
			return true
		}
	}
	return false
}

// proactiveSession selects the explicit source conversation when possible,
// otherwise the most recently written conversation for the same role. It does
// not create a conversation merely because a background opportunity fired.
func (s *Server) proactiveSession(role, preferred string) (string, *probe.Session, error) {
	entries, err := s.runtime.Entries()
	if err != nil {
		return "", nil, err
	}
	ordered := make([]string, 0, len(entries)+1)
	if preferred != "" && validID(preferred) {
		ordered = append(ordered, preferred)
	}
	for _, entry := range entries {
		if entry.RoleID == role && entry.ID != preferred {
			ordered = append(ordered, entry.ID)
		}
	}
	var fallbackID string
	var fallback *probe.Session
	for _, id := range ordered {
		sess, openErr := s.session(id)
		if openErr != nil || sess.RoleID() != role {
			continue
		}
		if id == preferred || hasUserConversation(sess.Messages()) {
			return id, sess, nil
		}
		if fallback == nil {
			fallbackID, fallback = id, sess
		}
	}
	if fallback != nil {
		return fallbackID, fallback, nil
	}
	return "", nil, errProactiveUnavailable
}

func (s *Server) beginProactive(parent context.Context, role, preferred string) (*proactiveTarget, error) {
	id, sess, err := s.proactiveSession(role, preferred)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(s.workCtx)
	if parent == nil {
		parent = context.Background()
	}
	stopParent := context.AfterFunc(parent, cancel)
	state := &runState{cancel: cancel, done: make(chan struct{})}
	s.mu.Lock()
	if s.closing || s.runs[id] != nil || len(s.runs) >= 4 {
		s.mu.Unlock()
		stopParent()
		cancel()
		return nil, errProactiveUnavailable
	}
	pending, _, queueErr := s.turns.HasQueued(id)
	if queueErr != nil || pending {
		s.mu.Unlock()
		stopParent()
		cancel()
		if queueErr != nil {
			return nil, queueErr
		}
		return nil, errProactiveUnavailable
	}
	s.runs[id] = state
	s.mu.Unlock()
	var once sync.Once
	release := func() {
		once.Do(func() {
			stopParent()
			cancel()
			s.mu.Lock()
			if s.runs[id] == state {
				delete(s.runs, id)
				close(state.done)
			}
			s.mu.Unlock()
		})
	}
	return &proactiveTarget{id: id, session: sess, ctx: ctx, release: release}, nil
}

func proactiveRequestID(kind, id string) (string, error) {
	value := "followup_" + kind + "_" + id
	if !validID(value) {
		return "", errors.New("invalid proactive trigger id")
	}
	return value, nil
}

func (s *Server) runProactive(parent context.Context, role, preferred, kind, triggerID string, trigger any, instruction string, outputLimit int, timeout time.Duration, finalize proactiveFinalizer) (targetID string, message probe.Message, persisted bool, runErr error) {
	encoded, err := json.Marshal(trigger)
	if err != nil || len(encoded) > 16<<10 || !json.Valid(encoded) || finalize == nil {
		return "", probe.Message{}, false, errors.New("invalid proactive trigger")
	}
	requestID, err := proactiveRequestID(kind, triggerID)
	if err != nil {
		return "", probe.Message{}, false, err
	}
	target, err := s.beginProactive(parent, role, preferred)
	if err != nil {
		return "", probe.Message{}, false, err
	}
	defer target.release()
	recorder, err := runlog.Open(s.runtime.Root(), runlog.Scope{SessionID: target.id, RoleID: role, RequestID: requestID})
	if err != nil {
		return target.id, probe.Message{}, false, err
	}
	defer func() {
		if closeErr := recorder.Close(); closeErr != nil {
			runErr = errors.Join(runErr, closeErr)
		}
	}()
	agent, err := s.agentForTurn(target.session, []string{}, target.id, func(c *probe.Config) {
		c.MaxTurns = 1
		c.MaxToolCalls = 1
		if c.MaxOutputTokens == 0 || c.MaxOutputTokens > outputLimit {
			c.MaxOutputTokens = outputLimit
		}
		c.Timeout = min(c.Timeout, timeout)
		c.Thinking = ""
		c.ReasoningEffort = ""
		c.RequestID = requestID
		c.Recorder = recorder
		c.Reference = string(encoded)
		c.HostPrompt = proactiveHostPolicy + "\n" + instruction
	})
	if err != nil {
		return target.id, probe.Message{}, false, err
	}
	message, persisted, err = probe.RunFollowup(target.ctx, agent, timeout, func(response probe.Message, messageIndex int) (probe.Message, bool, error) {
		return finalize(target.id, response, messageIndex)
	}, nil)
	if err != nil {
		return target.id, message, persisted, fmt.Errorf("proactive followup: %w", err)
	}
	return target.id, message, persisted, nil
}
