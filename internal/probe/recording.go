package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrRecording marks a failure in the host-owned durable execution record.
// Callers must not turn it into an automatic model or tool retry.
var ErrRecording = errors.New("runtime recording failed")

type ReceiptCapability string

const (
	ReceiptNone   ReceiptCapability = "none"
	ReceiptAtomic ReceiptCapability = "atomic"
)

type BusinessReceipt struct {
	Store              string   `json:"store"`
	RoleID             string   `json:"role_id"`
	OperationID        string   `json:"operation_id"`
	ArgumentsSHA256    string   `json:"arguments_sha256"`
	EntityIDs          []string `json:"entity_ids"`
	ResultingRevisions []int    `json:"resulting_revisions"`
	OutcomeDigest      string   `json:"outcome_digest"`
}

// RunRecorder is the small persistence seam used by Harness phase 3B. Its
// lifecycle and pre-dispatch ordering follow DeepSeek Harness (MIT), while the
// storage implementation and business receipts remain native Go code.
//
// ModelRequested and ToolStarted must durably finish before their external
// operation starts. A failure from ModelFinished or ToolFinished never changes
// an already observed provider/tool result into a rolled-back operation.
type RunRecorder interface {
	AttemptStarted(context.Context, AttemptRecord) error
	ModelRequested(context.Context, int, ModelRequest) error
	ModelFinished(context.Context, int, Message, error) error
	ToolStarted(context.Context, int, ToolCall, json.RawMessage, ReceiptCapability) (string, error)
	ToolFinished(context.Context, int, ToolCall, string, ToolResult) error
	AttemptFinished(context.Context, AttemptFinish) error
}

type AttemptRecord struct {
	SessionID   string    `json:"session_id"`
	RoleID      string    `json:"role_id"`
	RequestID   string    `json:"request_id"`
	HistoryFrom int       `json:"history_from"`
	Inputs      []Message `json:"inputs"`
	MaxTurns    int       `json:"max_turns"`
	MaxTools    int       `json:"max_tools"`
}

type AttemptFinish struct {
	Status     string `json:"status"`
	StopReason string `json:"stop_reason"`
	Error      string `json:"error,omitempty"`
}

type toolOperationKey struct{}

// ToolOperationID is the host-generated durable identity for a dispatched
// tool operation. It is empty when phase-3B recording is not enabled.
func ToolOperationID(ctx context.Context) string {
	id, _ := ctx.Value(toolOperationKey{}).(string)
	return id
}

func recordingError(stage string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %s: %v", ErrRecording, stage, err)
}
