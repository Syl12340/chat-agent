package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"pocket-agent/internal/companion"
	"pocket-agent/internal/interactive"
	"pocket-agent/internal/probe"
	"pocket-agent/internal/retrieval"
)

const recentRecallUserMessages = 3

// proactiveTurnContext adds one host-owned trigger to the normal role/session
// reference and narrows history to a recent, user-anchored window. The trigger
// remains inside the ephemeral runtime snapshot, never a persisted user turn.
func proactiveTurnContext(base func(context.Context, probe.StepContext) (probe.StepState, error), sess *probe.Session, trigger string) func(context.Context, probe.StepContext) (probe.StepState, error) {
	return func(ctx context.Context, step probe.StepContext) (probe.StepState, error) {
		state, err := base(ctx, step)
		if err != nil {
			return probe.StepState{}, err
		}
		var reference map[string]any
		var value any
		if json.Unmarshal([]byte(state.Reference), &reference) != nil || reference == nil || json.Unmarshal([]byte(trigger), &value) != nil || value == nil {
			return probe.StepState{}, errors.New("invalid proactive trigger reference")
		}
		reference["proactive_trigger"] = value
		encoded, err := json.Marshal(reference)
		if err != nil || len(encoded) > 24<<10 {
			return probe.StepState{}, errors.New("proactive trigger reference exceeds limit")
		}
		messages := sess.Messages()
		floor := max(state.HistoryFloor, len(messages)-12)
		for floor < len(messages) && messages[floor].Role != "user" {
			floor++
		}
		return probe.StepState{Reference: string(encoded), HistoryFloor: floor}, nil
	}
}

// continuityRecallQuery keeps a concrete current subject authoritative. A
// purely referential follow-up may borrow the nearest visible user subject so
// approved memories/events can still be found without another model call.
// Hidden history and structured UI envelopes never become retrieval queries.
func continuityRecallQuery(current string, history []probe.Message, floor, end int) string {
	current = strings.TrimSpace(current)
	if retrieval.HasTerms(current) {
		return current
	}
	end = min(max(end, 0), len(history))
	floor = min(max(floor, 0), end)
	seen := 0
	for i := end - 1; i >= floor && seen < recentRecallUserMessages; i-- {
		message := history[i]
		if message.Role != "user" || message.Source != "" {
			continue
		}
		seen++
		candidate := strings.TrimSpace(message.Content)
		_, reserved, _ := interactive.ParseResponse(candidate)
		if candidate == "" || reserved || !retrieval.HasTerms(candidate) {
			continue
		}
		runes := []rune(candidate)
		if len(runes) > 512 {
			candidate = string(runes[:256]) + " " + string(runes[len(runes)-256:])
		}
		return candidate
	}
	return current
}

// One closure belongs to one turn. The cache keys actual role-scoped data,
// never elapsed time: another step must not repeat a paid rerank just because
// its budget shrank. Changed/deleted records invalidate the complete reference.
func (s *Server) turnContext(sess *probe.Session, role, sessionID, query string) func(context.Context, probe.StepContext) (probe.StepState, error) {
	historyEnd := len(sess.Messages())
	var mu sync.Mutex
	var previous [32]byte
	var cached string
	ready := false
	return func(ctx context.Context, step probe.StepContext) (probe.StepState, error) {
		mu.Lock()
		defer mu.Unlock()
		if err := ctx.Err(); err != nil {
			return probe.StepState{}, err
		}
		if step.RoleID != role || step.SessionID != sessionID || sess.RoleID() != role {
			return probe.StepState{}, errors.New("runtime context scope mismatch")
		}
		preferences, err := s.companion.Get(role)
		if err != nil {
			return probe.StepState{}, err
		}
		activity, err := s.activities.Session(role, sessionID)
		if err != nil {
			return probe.StepState{}, err
		}
		facts, err := s.memory.List(role)
		if err != nil {
			return probe.StepState{}, err
		}
		approved := facts[:0]
		for _, fact := range facts {
			if fact.Status == "approved" {
				approved = append(approved, fact)
			}
		}
		events, err := s.life.Episodes(role)
		if err != nil {
			return probe.StepState{}, err
		}
		visible := events[:0]
		for _, event := range events {
			if event.Status == "open" || event.Status == "resolved" {
				visible = append(visible, event)
			}
		}
		activities, err := s.activities.List(role)
		if err != nil {
			return probe.StepState{}, err
		}
		lifeFloor, err := s.life.Floor(role, sessionID)
		if err != nil {
			return probe.StepState{}, err
		}
		activityFloor, err := s.activities.Floor(role, sessionID)
		if err != nil {
			return probe.StepState{}, err
		}
		floor := max(lifeFloor, activityFloor)
		recallQuery := continuityRecallQuery(query, sess.Messages(), floor, historyEnd)
		// Include review status/revisions and bindings so corrections, forgetting,
		// pausing or switching activities cannot resurrect an initial snapshot.
		signature, err := json.Marshal([]any{preferences, activity, approved, visible, activities, floor})
		if err != nil {
			return probe.StepState{}, err
		}
		digest := sha256.Sum256(signature)
		if !ready || digest != previous {
			_, prefJSON := companion.Context(preferences, "auto", role == "companion")
			var activityState map[string]any
			active := activity
			if activity != nil {
				activityState = map[string]any{"id": activity.ID, "revision": activity.Revision, "status": activity.Status}
				if activity.Status != "active" {
					active = nil
				}
			}
			text, err := s.recallReferenceContext(ctx, role, recallQuery, prefJSON, active, activityState)
			if err != nil {
				return probe.StepState{}, err
			}
			cached, previous, ready = text, digest, true
		}
		care, err := s.life.Settings(role)
		if err != nil {
			return probe.StepState{}, err
		}
		data, err := json.Marshal(map[string]any{
			"saved_context": json.RawMessage(cached), "care_settings": care,
			"timezone": s.location.String(), "local_time": time.Now().In(s.location).Format(time.RFC3339),
		})
		if err != nil {
			return probe.StepState{}, err
		}
		if err := ctx.Err(); err != nil {
			return probe.StepState{}, err
		}
		return probe.StepState{Reference: string(data), HistoryFloor: floor}, nil
	}
}
