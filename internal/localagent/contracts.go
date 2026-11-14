// Package localagent implements the Windows-side workspace endpoint.
//
// It is intentionally a workspace guard, not an operating-system sandbox.
// Files are resolved through os.Root and commands are selected from a fixed
// operator allow-list. The package never invokes a shell or privilege broker.
package localagent

import "time"

const Protocol = "ca.local-agent.v1"

// Kinds lists the task kinds the executor accepts. It is exported so the error a
// caller gets for an unknown kind can name the alternatives instead of only
// saying that something was unsupported.
var Kinds = []string{"list", "read", "search", "share", "write", "exec"}

type Task struct {
	Protocol    string `json:"protocol"`
	OperationID string `json:"operation_id"`
	// Kind is one of Kinds. "exec" runs the named entry of the operator's fixed
	// action allow-list; it is not a command line and takes no arguments.
	Kind    string `json:"kind"`
	Path    string `json:"path,omitempty"`
	Query   string `json:"query,omitempty"`
	Action  string `json:"action,omitempty"`
	Content string `json:"content,omitempty"`
	// ExpectedSHA256 is either the current lowercase SHA-256 or "missing".
	// Requiring it makes every write an explicit compare-and-swap operation.
	ExpectedSHA256 string   `json:"expected_sha256,omitempty"`
	Artifacts      []string `json:"artifacts,omitempty"`
}

type Artifact struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Bytes     int64  `json:"bytes"`
	SHA256    string `json:"sha256"`
}

type Receipt struct {
	Protocol      string         `json:"protocol"`
	OperationID   string         `json:"operation_id"`
	RequestSHA256 string         `json:"request_sha256"`
	Status        string         `json:"status"`
	Output        string         `json:"output,omitempty"`
	Entries       []string       `json:"entries,omitempty"`
	Artifacts     []Artifact     `json:"artifacts,omitempty"`
	Error         string         `json:"error,omitempty"`
	StartedAt     time.Time      `json:"started_at"`
	CompletedAt   time.Time      `json:"completed_at"`
	Guard         map[string]any `json:"guard"`
}
