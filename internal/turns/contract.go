package turns

import (
	"encoding/json"
	"net/url"
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
			Operations: operationsOf(sessionID, turn.Tools),
		}},
	}
}

func operationsOf(sessionID string, tools []ToolEvent) []Operation {
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
		operation.Artifacts = artifactsOf(sessionID, event)
		out = append(out, operation)
	}
	return out
}

// artifactsOf reads results that outlive the reply. Only an explicit structured
// field counts: guessing from prose would invent files that were never produced.
//
// The link is derived from the session, never copied from the tool. Only the host
// knows how to serve these bytes to this conversation, and only the host's own
// routes sit behind the API's authentication. A tool-reported absolute URL points
// somewhere else, so the artifact is kept without a link instead of being shown as
// a button that cannot work.
func artifactsOf(sessionID string, event ToolEvent) []Artifact {
	kind := artifactKind(event.Name)
	if kind == "" {
		return nil
	}
	var payload struct {
		Path        string `json:"path"`
		Name        string `json:"name"`
		URL         string `json:"url"`
		DownloadURL string `json:"download_url"`
		ImageID     string `json:"image_id"`
		SHA256      string `json:"sha256"`
	}
	trimmed := strings.TrimSpace(event.Output)
	if !strings.HasPrefix(trimmed, "{") || json.Unmarshal([]byte(trimmed), &payload) != nil {
		return nil
	}
	ref := firstNonEmpty(payload.Path, payload.Name, payload.ImageID)
	if ref == "" {
		return nil
	}
	return []Artifact{{
		Kind:     kind,
		Ref:      ref,
		SHA256:   payload.SHA256,
		Download: artifactDownload(sessionID, payload.DownloadURL, payload.URL, payload.ImageID, payload.Path, payload.SHA256),
	}}
}

// artifactDownload returns a route this host will actually serve. Reported routes
// are only trusted when they are already relative, which means the host built
// them; an absolute URL is somebody else's address.
func artifactDownload(sessionID, downloadURL, rawURL, imageID, path, digest string) string {
	if route := firstNonEmpty(downloadURL, rawURL); strings.HasPrefix(route, "/") {
		return route
	}
	scoped := "/api/sessions/" + url.PathEscape(sessionID)
	if imageID != "" {
		return scoped + "/images/" + url.PathEscape(imageID)
	}
	// A written file has no link of its own, but the digest it reported is exactly
	// what the download route verifies against.
	if path != "" && digest != "" {
		return scoped + "/workspace-files/" + url.PathEscape(digest) + "?path=" + url.QueryEscape(path)
	}
	return ""
}

// artifactKind is the allow-list: a tool that cannot produce a deliverable has no
// kind, so its output is never interpreted.
func artifactKind(tool string) string {
	switch tool {
	case "image_generate", "image_status":
		return "image"
	case "workspace_share", "workspace_write":
		return "file"
	default:
		return ""
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
