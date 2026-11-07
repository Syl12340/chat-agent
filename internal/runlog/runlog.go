// Package runlog implements the Harness runtime journal and its phase-3C
// manifest-selected storage registry. Transcript-era files are accepted only
// by the explicit stopped-server migration path.
package runlog

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"pocket-agent/internal/probe"
	"pocket-agent/internal/storage"
)

const (
	Version           = 2
	Contract          = "harness-storage/1"
	MaxEventBytes     = 256 << 10
	MaxSnapshotBytes  = 128 << 20
	MaxPayloadBytes   = 64 << 20
	MaxGenerationSize = 1 << 30
)

type Scope struct {
	SessionID string
	RoleID    string
	RequestID string
}

type Reference struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type Event struct {
	Version        int             `json:"version"`
	Contract       string          `json:"contract"`
	EventID        string          `json:"event_id"`
	EventSeq       uint64          `json:"event_seq"`
	Type           string          `json:"type"`
	At             time.Time       `json:"at"`
	SessionID      string          `json:"session_id"`
	RoleID         string          `json:"role_id"`
	RequestID      string          `json:"request_id"`
	AttemptID      string          `json:"attempt_id,omitempty"`
	StepID         string          `json:"step_id,omitempty"`
	CallID         string          `json:"call_id,omitempty"`
	ProviderCallID string          `json:"provider_call_id,omitempty"`
	OperationID    string          `json:"operation_id,omitempty"`
	Reference      *Reference      `json:"reference,omitempty"`
	Data           json.RawMessage `json:"data,omitempty"`
}

type document struct {
	Version   int             `json:"version"`
	Contract  string          `json:"contract"`
	Kind      string          `json:"kind"`
	CreatedAt time.Time       `json:"created_at"`
	Value     json.RawMessage `json:"value"`
}

type toolState struct {
	stepID      string
	callID      string
	operationID string
	capability  probe.ReceiptCapability
}

// Recorder is a single-writer, fail-closed execution recorder. Each append is
// synced before success is returned. This ordering is adapted from DeepSeek
// Harness (MIT); its runtime is not embedded.
type Recorder struct {
	mu        sync.Mutex
	scope     Scope
	dir       string
	lockPath  string
	file      *os.File
	seq       uint64
	used      int64
	failed    error
	closed    bool
	attemptID string
	active    bool
	steps     map[int]string
	pending   map[int]bool
	tools     map[string]toolState
	now       func() time.Time
}

func Open(root string, scope Scope) (*Recorder, error) {
	if !storage.ValidID(scope.SessionID) || !storage.ValidID(scope.RoleID) || !storage.ValidID(scope.RequestID) {
		return nil, errors.New("invalid runlog scope")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	dir := filepath.Join(root, scope.SessionID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	var m Manifest
	data, err := os.ReadFile(manifestPath)
	if errors.Is(err, os.ErrNotExist) {
		m = Manifest{Version: Version, Contract: Contract, SessionID: scope.SessionID, RoleID: scope.RoleID, Generation: 1, ImportedFrom: "isolated-3b"}
		encoded, encodeErr := json.MarshalIndent(m, "", "  ")
		if encodeErr != nil {
			return nil, encodeErr
		}
		if err = storage.Replace(manifestPath, append(encoded, '\n')); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else if len(data) > 64<<10 || decodeStrict(data, &m) != nil || m.Version != Version || m.Contract != Contract || m.SessionID != scope.SessionID || m.RoleID != scope.RoleID || m.Generation < 1 || m.ImportedFrom == "" {
		return nil, errors.New("invalid runlog manifest")
	}
	generation := filepath.Join(dir, generationName(m.Generation))
	for _, name := range []string{generation, filepath.Join(generation, "snapshots"), filepath.Join(generation, "payloads")} {
		if err := os.MkdirAll(name, 0700); err != nil {
			return nil, err
		}
	}
	lockPath := filepath.Join(generation, ".writer.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("runlog writer unavailable: %w", err)
	}
	_, writeErr := fmt.Fprintf(lock, "pid=%d request=%s\n", os.Getpid(), scope.RequestID)
	lockErr := errors.Join(writeErr, lock.Close())
	if lockErr != nil {
		_ = os.Remove(lockPath)
		return nil, lockErr
	}
	keepLock := false
	defer func() {
		if !keepLock {
			_ = os.Remove(lockPath)
		}
	}()
	used, err := directorySize(generation)
	if err != nil || used > MaxGenerationSize {
		if err == nil {
			err = errors.New("runlog session capacity exceeded")
		}
		return nil, err
	}
	eventsPath := filepath.Join(generation, "events.jsonl")
	seq, err := inspectEvents(eventsPath, scope)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	keepLock = true
	return &Recorder{scope: scope, dir: generation, lockPath: lockPath, file: f, seq: seq, used: used, steps: map[int]string{}, pending: map[int]bool{}, tools: map[string]toolState{}, now: func() time.Time { return time.Now().UTC() }}, nil
}

func decodeStrict(data []byte, value any) error {
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}

func directorySize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("runlog contains a symbolic link")
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			total += info.Size()
			if total > MaxGenerationSize {
				return errors.New("runlog session capacity exceeded")
			}
		}
		return nil
	})
	return total, err
}

func inspectEvents(path string, scope Scope) (uint64, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if info.Size() > 0 {
		last := []byte{0}
		if _, err := f.ReadAt(last, info.Size()-1); err != nil || last[0] != '\n' {
			return 0, errors.New("runlog has a torn final event")
		}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), MaxEventBytes)
	var seq uint64
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if len(line)+1 > MaxEventBytes {
			return 0, errors.New("runlog event exceeds capacity")
		}
		var event Event
		if decodeStrict(line, &event) != nil || !validEvent(event, seq+1, scope.SessionID, scope.RoleID) {
			return 0, errors.New("invalid runlog event")
		}
		seq++
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return seq, nil
}

func validEvent(event Event, sequence uint64, sessionID, roleID string) bool {
	if event.Version != Version || event.Contract != Contract || event.EventSeq != sequence || event.At.IsZero() || !storage.ValidID(event.EventID) || !storage.ValidID(event.SessionID) || !storage.ValidID(event.RoleID) || !storage.ValidID(event.RequestID) || sessionID != "" && event.SessionID != sessionID || roleID != "" && event.RoleID != roleID {
		return false
	}
	for _, id := range []string{event.AttemptID, event.StepID, event.CallID, event.OperationID} {
		if id != "" && !storage.ValidID(id) {
			return false
		}
	}
	if len(event.Data) > 0 && !json.Valid(event.Data) {
		return false
	}
	kind := ""
	if event.Reference != nil {
		ref := event.Reference
		if !storage.ValidID(ref.ID) || !validHexDigest(ref.SHA256) || ref.Bytes < 1 {
			return false
		}
		group, limit := "payloads", int64(MaxPayloadBytes)
		if ref.Kind == "model_request" {
			group, limit = "snapshots", int64(MaxSnapshotBytes)
		} else if ref.Kind != "attempt_start" && ref.Kind != "model_result" && ref.Kind != "tool_request" && ref.Kind != "tool_result" {
			return false
		}
		want := filepath.ToSlash(filepath.Join(group, ref.ID+".json"))
		if ref.Path != want || ref.Bytes > limit {
			return false
		}
		kind = ref.Kind
	}
	switch event.Type {
	case "attempt.started":
		return event.AttemptID != "" && event.StepID == "" && event.CallID == "" && event.OperationID == "" && kind == "attempt_start"
	case "model.requested":
		return event.AttemptID != "" && event.StepID != "" && event.CallID == "" && event.OperationID == "" && kind == "model_request"
	case "model.finished":
		return event.AttemptID != "" && event.StepID != "" && event.CallID == "" && event.OperationID == "" && kind == "model_result"
	case "tool.started":
		return event.AttemptID != "" && event.StepID != "" && event.CallID != "" && event.ProviderCallID != "" && event.OperationID != "" && kind == "tool_request"
	case "tool.finished":
		return event.AttemptID != "" && event.StepID != "" && event.CallID != "" && event.ProviderCallID != "" && event.OperationID != "" && kind == "tool_result" && len(event.Data) > 0
	case "attempt.finished":
		return event.AttemptID != "" && event.StepID == "" && event.CallID == "" && event.OperationID == "" && event.Reference == nil && len(event.Data) > 0
	default:
		return false
	}
}

func validHexDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func validBusinessReceipt(receipt *probe.BusinessReceipt, roleID, operationID string) bool {
	if receipt == nil || !storage.ValidID(receipt.Store) || receipt.RoleID != roleID || receipt.OperationID != operationID || !validHexDigest(receipt.ArgumentsSHA256) || !validHexDigest(receipt.OutcomeDigest) || len(receipt.EntityIDs) == 0 || len(receipt.EntityIDs) > 8 || len(receipt.EntityIDs) != len(receipt.ResultingRevisions) {
		return false
	}
	for i, id := range receipt.EntityIDs {
		if !storage.ValidID(id) || receipt.ResultingRevisions[i] < 1 {
			return false
		}
	}
	return true
}

func randomID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func (r *Recorder) failLocked(err error) error {
	if err == nil {
		return nil
	}
	if r.failed == nil {
		r.failed = err
		if r.file != nil {
			_ = r.file.Close()
			r.file = nil
		}
	}
	return r.failed
}

func (r *Recorder) readyLocked() error {
	if r.failed != nil {
		return r.failed
	}
	if r.closed || r.file == nil {
		return errors.New("runlog recorder closed")
	}
	return nil
}

func (r *Recorder) appendLocked(event Event) error {
	if err := r.readyLocked(); err != nil {
		return err
	}
	id, err := randomID()
	if err != nil {
		return r.failLocked(err)
	}
	event.Version, event.Contract = Version, Contract
	event.EventID, event.EventSeq, event.At = id, r.seq+1, r.now()
	event.SessionID, event.RoleID, event.RequestID = r.scope.SessionID, r.scope.RoleID, r.scope.RequestID
	data, err := json.Marshal(event)
	if err != nil || len(data)+1 > MaxEventBytes || r.used+int64(len(data)+1) > MaxGenerationSize {
		if err == nil {
			err = errors.New("runlog event capacity exceeded")
		}
		return r.failLocked(err)
	}
	data = append(data, '\n')
	n, err := r.file.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = r.file.Sync()
	}
	if err != nil {
		return r.failLocked(err)
	}
	r.seq++
	r.used += int64(len(data))
	return nil
}

func (r *Recorder) writeDocumentLocked(group, kind string, value any, limit int) (*Reference, error) {
	if err := r.readyLocked(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, r.failLocked(err)
	}
	doc, err := json.Marshal(document{Version: Version, Contract: Contract, Kind: kind, CreatedAt: r.now(), Value: raw})
	if err != nil || len(doc)+1 > limit || r.used+int64(len(doc)+1) > MaxGenerationSize {
		if err == nil {
			err = errors.New("runlog document capacity exceeded")
		}
		return nil, r.failLocked(err)
	}
	id, err := randomID()
	if err != nil {
		return nil, r.failLocked(err)
	}
	rel := filepath.ToSlash(filepath.Join(group, id+".json"))
	data := append(doc, '\n')
	if err := storage.Replace(filepath.Join(r.dir, filepath.FromSlash(rel)), data); err != nil {
		return nil, r.failLocked(err)
	}
	digest := sha256.Sum256(data)
	r.used += int64(len(data))
	return &Reference{ID: id, Kind: kind, Path: rel, SHA256: hex.EncodeToString(digest[:]), Bytes: int64(len(data))}, nil
}

func (r *Recorder) AttemptStarted(_ context.Context, input probe.AttemptRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active || input.SessionID != r.scope.SessionID || input.RoleID != r.scope.RoleID || input.RequestID != r.scope.RequestID {
		return r.failLocked(errors.New("invalid attempt start"))
	}
	id, err := randomID()
	if err != nil {
		return r.failLocked(err)
	}
	r.attemptID = id
	ref, err := r.writeDocumentLocked("payloads", "attempt_start", input, MaxPayloadBytes)
	if err != nil {
		return err
	}
	if err := r.appendLocked(Event{Type: "attempt.started", AttemptID: id, Reference: ref}); err != nil {
		return err
	}
	r.active = true
	return nil
}

func (r *Recorder) ModelRequested(_ context.Context, step int, request probe.ModelRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active || step < 0 {
		return r.failLocked(errors.New("model request outside active attempt"))
	}
	if _, exists := r.steps[step]; exists {
		return r.failLocked(errors.New("duplicate model step"))
	}
	stepID, err := randomID()
	if err != nil {
		return r.failLocked(err)
	}
	ref, err := r.writeDocumentLocked("snapshots", "model_request", request, MaxSnapshotBytes)
	if err != nil {
		return err
	}
	r.steps[step] = stepID
	if err := r.appendLocked(Event{Type: "model.requested", AttemptID: r.attemptID, StepID: stepID, Reference: ref}); err != nil {
		return err
	}
	r.pending[step] = true
	return nil
}

func (r *Recorder) ModelFinished(_ context.Context, step int, response probe.Message, runErr error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	stepID := r.steps[step]
	if !r.active || stepID == "" || !r.pending[step] {
		return r.failLocked(errors.New("model finish without request"))
	}
	status := "complete"
	if runErr != nil {
		status = "error"
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			status = "canceled"
		}
	}
	value := struct {
		Status   string        `json:"status"`
		Response probe.Message `json:"response"`
		Error    string        `json:"error,omitempty"`
	}{Status: status, Response: response}
	if runErr != nil {
		value.Error = bound(runErr.Error(), 4096)
	}
	ref, err := r.writeDocumentLocked("payloads", "model_result", value, MaxPayloadBytes)
	if err != nil {
		return err
	}
	if err := r.appendLocked(Event{Type: "model.finished", AttemptID: r.attemptID, StepID: stepID, Reference: ref}); err != nil {
		return err
	}
	delete(r.pending, step)
	return nil
}

func (r *Recorder) ToolStarted(_ context.Context, step int, call probe.ToolCall, arguments json.RawMessage, capability probe.ReceiptCapability) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stepID := r.steps[step]
	if !r.active || stepID == "" || call.ID == "" || capability != probe.ReceiptNone && capability != probe.ReceiptAtomic {
		return "", r.failLocked(errors.New("invalid tool start"))
	}
	if _, exists := r.tools[call.ID]; exists {
		return "", r.failLocked(errors.New("duplicate provider tool call"))
	}
	callID, err := randomID()
	if err != nil {
		return "", r.failLocked(err)
	}
	operationID, err := randomID()
	if err != nil {
		return "", r.failLocked(err)
	}
	value := struct {
		Name              string                  `json:"name"`
		Arguments         json.RawMessage         `json:"arguments"`
		ReceiptCapability probe.ReceiptCapability `json:"receipt_capability"`
	}{Name: call.Function.Name, Arguments: append(json.RawMessage(nil), arguments...), ReceiptCapability: capability}
	ref, err := r.writeDocumentLocked("payloads", "tool_request", value, MaxPayloadBytes)
	if err != nil {
		return "", err
	}
	if err := r.appendLocked(Event{Type: "tool.started", AttemptID: r.attemptID, StepID: stepID, CallID: callID, ProviderCallID: call.ID, OperationID: operationID, Reference: ref}); err != nil {
		return "", err
	}
	r.tools[call.ID] = toolState{stepID: stepID, callID: callID, operationID: operationID, capability: capability}
	return operationID, nil
}

func (r *Recorder) ToolFinished(_ context.Context, step int, call probe.ToolCall, operationID string, result probe.ToolResult) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.tools[call.ID]
	if !r.active || !ok || state.stepID != r.steps[step] || state.operationID != operationID {
		return r.failLocked(errors.New("tool finish without matching start"))
	}
	ref, err := r.writeDocumentLocked("payloads", "tool_result", result, MaxPayloadBytes)
	if err != nil {
		return err
	}
	settlement := "untracked"
	if state.capability == probe.ReceiptAtomic {
		settlement = "unknown"
		if validBusinessReceipt(result.Receipt, r.scope.RoleID, operationID) {
			settlement = "committed"
		}
	}
	data, _ := json.Marshal(struct {
		Settlement string `json:"settlement"`
	}{settlement})
	if err := r.appendLocked(Event{Type: "tool.finished", AttemptID: r.attemptID, StepID: state.stepID, CallID: state.callID, ProviderCallID: call.ID, OperationID: operationID, Reference: ref, Data: data}); err != nil {
		return err
	}
	delete(r.tools, call.ID)
	return nil
}

func (r *Recorder) AttemptFinished(_ context.Context, finish probe.AttemptFinish) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active || finish.Status == "" || finish.StopReason == "" || len(r.pending) != 0 || len(r.tools) != 0 {
		return r.failLocked(errors.New("invalid attempt finish"))
	}
	finish.Error = bound(finish.Error, 4096)
	data, err := json.Marshal(finish)
	if err != nil {
		return r.failLocked(err)
	}
	if err := r.appendLocked(Event{Type: "attempt.finished", AttemptID: r.attemptID, Data: data}); err != nil {
		return err
	}
	r.active = false
	return nil
}

func bound(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return r.failed
	}
	r.closed = true
	closeErr := error(nil)
	if r.file != nil {
		closeErr = r.file.Close()
		r.file = nil
	}
	if r.failed == nil && r.active {
		r.failed = errors.New("runlog closed with an unfinished attempt")
	}
	if r.failed != nil {
		return errors.Join(r.failed, closeErr)
	}
	if closeErr != nil {
		r.failed = closeErr
		return closeErr
	}
	if err := os.Remove(r.lockPath); err != nil {
		r.failed = err
		return err
	}
	return r.failed
}

// ReadEvents is a bounded diagnostic reader for isolated 3B tests and future
// 3C projection work. It never repairs or skips invalid records.
func ReadEvents(root, sessionID string) ([]Event, error) {
	if !storage.ValidID(sessionID) {
		return nil, errors.New("invalid session id")
	}
	manifestData, err := os.ReadFile(filepath.Join(root, sessionID, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var manifest Manifest
	if len(manifestData) > 64<<10 || decodeStrict(manifestData, &manifest) != nil || manifest.Version != Version || manifest.Contract != Contract || manifest.SessionID != sessionID || manifest.Generation < 1 || manifest.ImportedFrom == "" {
		return nil, errors.New("invalid runlog manifest")
	}
	path := filepath.Join(root, sessionID, generationName(manifest.Generation), "events.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > 0 {
		last := []byte{0}
		if _, err := f.ReadAt(last, info.Size()-1); err != nil || last[0] != '\n' {
			return nil, errors.New("runlog has a torn final event")
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), MaxEventBytes)
	events := []Event{}
	for scanner.Scan() {
		var event Event
		roleID := ""
		if len(events) > 0 {
			roleID = events[0].RoleID
		}
		if len(scanner.Bytes())+1 > MaxEventBytes || decodeStrict(scanner.Bytes(), &event) != nil || !validEvent(event, uint64(len(events)+1), sessionID, roleID) {
			return nil, fmt.Errorf("invalid event at sequence %d", len(events)+1)
		}
		events = append(events, event)
		if len(events) > 1_000_000 {
			return nil, errors.New("too many runlog events")
		}
	}
	return events, scanner.Err()
}
