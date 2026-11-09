package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"chat-agent/internal/companion"
	"chat-agent/internal/memory"
	"chat-agent/internal/retrieval"
)

const recallContextBytes = 16 << 10

// Keep complete selected records rather than clipping a correction or saved
// progress mid-sentence. This is an ephemeral projection, not a new data format.
func referenceJSON(value any) []byte {
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	return bytes.TrimSpace(data.Bytes())
}

func (s *Server) relatedActivities(role, query, boundID string, limit int) ([]companion.Activity, error) {
	out := []companion.Activity{}
	if strings.TrimSpace(query) == "" {
		return out, nil
	}
	items, err := s.activities.List(role)
	if err != nil {
		return nil, err
	}
	docs := []retrieval.Document{}
	eligible := []companion.Activity{}
	for _, item := range items {
		if item.ID == boundID {
			continue
		}
		eligible = append(eligible, item)
		docs = append(docs, retrieval.Document{ID: item.ID, Text: item.Title + "\n" + item.Goal + "\n" + item.Progress, UpdatedAt: item.UpdatedAt})
	}
	for _, match := range retrieval.Rank(query, docs, limit) {
		out = append(out, eligible[match.Index])
	}
	return out, nil
}

func (s *Server) recallReference(role, query, preferences string, activity *companion.Activity, activityState map[string]any) (string, error) {
	return s.recallReferenceContext(context.Background(), role, query, preferences, activity, activityState)
}

func (s *Server) recallReferenceContext(ctx context.Context, role, query, preferences string, activity *companion.Activity, activityState map[string]any) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	facts := []memory.Entry{}
	events := []companion.Episode{}
	related := []companion.Activity{}
	if strings.TrimSpace(query) != "" {
		var err error
		facts, err = s.memory.SearchContext(ctx, role, query, 4)
		if err != nil {
			return "", err
		}
		events, err = s.life.Context(role, query)
		if err != nil {
			return "", err
		}
		boundID := ""
		if activity != nil {
			boundID = activity.ID
		}
		related, err = s.relatedActivities(role, query, boundID, 2)
		if err != nil {
			return "", err
		}
	}
	for i := range facts {
		facts[i].SessionID = ""
		if strings.HasPrefix(facts[i].Source, "merged:") {
			facts[i].Source = "merged"
		}
	}
	for i := range events {
		events[i].SourceText = ""
		events[i].SourceSessionID = ""
	}
	selectedFacts := []memory.Entry{}
	selectedEvents := []companion.Episode{}
	selectedActivities := []companion.Activity{}
	ref := map[string]any{
		"companion_preferences": json.RawMessage(preferences),
		"approved_memories":     selectedFacts, "events": selectedEvents,
		"shared_activity": activity, "activity_state": activityState,
		"related_activities": selectedActivities,
		"recall_scope":       "Selective current-role references, not an exhaustive history. related_activities are read-only; no automatic binding, resuming or progress changes.",
	}
	// Give each reference kind a turn so a handful of long facts cannot crowd
	// every event or shared experience out of the same bounded request.
	for i := 0; i < max(len(facts), min(len(events), 3), len(related)); i++ {
		if i < len(facts) {
			candidate := append(append([]memory.Entry{}, selectedFacts...), facts[i])
			ref["approved_memories"] = candidate
			if len(referenceJSON(ref)) <= recallContextBytes {
				selectedFacts = candidate
			} else {
				ref["approved_memories"] = selectedFacts
			}
		}
		if i < min(len(events), 3) {
			candidate := append(append([]companion.Episode{}, selectedEvents...), events[i])
			ref["events"] = candidate
			if len(referenceJSON(ref)) <= recallContextBytes {
				selectedEvents = candidate
			} else {
				ref["events"] = selectedEvents
			}
		}
		if i < len(related) {
			candidate := append(append([]companion.Activity{}, selectedActivities...), related[i])
			ref["related_activities"] = candidate
			if len(referenceJSON(ref)) <= recallContextBytes {
				selectedActivities = candidate
			} else {
				ref["related_activities"] = selectedActivities
			}
		}
	}
	data := referenceJSON(ref)
	if len(data) > recallContextBytes && activity != nil {
		// A maximum-size explicitly bound activity can itself fill the budget.
		// Keep its identity/status and require a tool read for the full content.
		ref["shared_activity"] = map[string]any{"id": activity.ID, "revision": activity.Revision, "status": activity.Status, "title": activity.Title, "details_omitted": true}
		data = referenceJSON(ref)
	}
	if len(data) > recallContextBytes {
		return "", errors.New("saved reference exceeds context budget")
	}
	return string(data), nil
}
