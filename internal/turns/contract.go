package turns

import (
	"encoding/json"
	"strings"
	"time"
)

// The four levels the plan asks for. They are a projection of what the turn
// already recorded, not a second store, so the view cannot drift from what
// actually happened. Everyone who needs task state - the page, the model, a
// channel - reads these instead of interpreting raw turn fields on its own.

// Task is what the user asked for, kept apart from how it was carried out.
type Task struct {
	ID      string    `json:"id"`
	Session string    `json:"session,omitempty"`
	Request string    `json:"request"`
	Status  string    `json:"status"`
	Error   string    `json:"error,omitempty"`
	Origin  string    `json:"origin,omitempty"`
	Created time.Time `json:"created_at"`
	Runs    []Run     `json:"runs"`
}

// Run is one model execution that tried to satisfy the task.
type Run struct {
	ID         string      `json:"id"`
	Model      string      `json:"model,omitempty"`
	Thinking   string      `json:"thinking,omitempty"`
	Status     string      `json:"status"`
	Reply      string      `json:"reply,omitempty"`
	Operations []Operation `json:"operations"`
}

// Operation is one actual action, including where it ran.
type Operation struct {
	ID        string     `json:"id"`
	Tool      string     `json:"tool"`
	Status    string     `json:"status"`
	Location  string     `json:"location,omitempty"`
	Error     string     `json:"error,omitempty"`
	Artifacts []Artifact `json:"artifacts,omitempty"`
}

// Artifact is something the task produced that outlives the reply.
type Artifact struct {
	Kind   string `json:"kind"`
	Ref    string `json:"ref"`
	SHA256 string `json:"sha256,omitempty"`
	// Download is a server path that serves the bytes, when one is known. An
	// artifact without it is still recorded - refusal to lose it is better than
	// dropping it because a link could not be derived.
	Download string `json:"download,omitempty"`
}

// ContractOf projects one turn into the four levels.
func ContractOf(sessionID string, turn Turn) Task {
	return Task{
		ID:      turn.ID,
		Session: sessionID,
		Request: turn.Content,
		Status:  turn.Status,
		Error:   turn.Error,
		Origin:  turn.Origin.Kind,
		Created: turn.CreatedAt,
		Runs: []Run{{
			ID:         turn.ID,
			Model:      turn.Model,
			Thinking:   turn.Thinking,
			Status:     turn.Status,
			Reply:      turn.Text,
			Operations: operationsOf(turn.Tools),
		}},
	}
}

func operationsOf(tools []ToolEvent) []Operation {
	out := make([]Operation, 0, len(tools))
	for _, event := range tools {
		status := event.Status
		if status == "" {
			// A tool that reported neither an outcome nor an error is treated as
			// having succeeded, because the turn only keeps finished events.
			if strings.TrimSpace(event.Error) != "" {
				status = "failed"
			} else {
				status = "ok"
			}
		}
		operation := Operation{ID: event.ID, Tool: event.Name, Status: status, Error: event.Error}
		if event.View != nil {
			// Where the action ran is part of the task, not a rendering detail:
			// the server workspace and a paired computer behave differently.
			operation.Location = event.View.Endpoint
		}
		operation.Artifacts = artifactsOf(event)
		out = append(out, operation)
	}
	return out
}

// artifactsOf reads results that outlive the reply. Only an explicit structured
// field counts: guessing from prose would invent files that were never produced.
func artifactsOf(event ToolEvent) []Artifact {
	switch event.Name {
	case "workspace_share", "workspace_write", "image_generate", "courseware_upload":
	default:
		return nil
	}
	var payload struct {
		Path   string `json:"path"`
		URL    string `json:"url"`
		SHA256 string `json:"sha256"`
		File   string `json:"file"`
		Entity string `json:"entity_id"`
	}
	trimmed := strings.TrimSpace(event.Output)
	if !strings.HasPrefix(trimmed, "{") || json.Unmarshal([]byte(trimmed), &payload) != nil {
		return nil
	}
	ref := firstNonEmpty(payload.Path, payload.URL, payload.File, payload.Entity)
	if ref == "" {
		return nil
	}
	kind := artifactKind(event.Name)
	artifact := Artifact{Kind: kind, Ref: ref, SHA256: payload.SHA256, Download: payload.URL}
	// An image the server holds always has a path to its bytes, even when the tool
	// only reported an identifier.
	if artifact.Download == "" && kind == "image" && payload.Entity != "" {
		artifact.Download = "/api/images/" + payload.Entity
	}
	return []Artifact{artifact}
}

func artifactKind(tool string) string {
	switch tool {
	case "image_generate":
		return "image"
	case "courseware_upload":
		return "courseware"
	default:
		return "file"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
