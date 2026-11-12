package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"chat-agent/internal/localagent"
	"chat-agent/internal/probe"
)

// LocalAgentConfig registers the Windows execution endpoints the host will accept.
//
// Devices are configured rather than registered at runtime: an endpoint that runs
// commands on a person's machine should be something an operator wrote down, not
// something that announced itself and was approved by a model.
type LocalAgentConfig struct {
	// Devices maps a device ID to its bearer token.
	Devices map[string]string
	// DefaultDevice receives tasks from the model when the tool does not name one.
	DefaultDevice string
	// Wait bounds how long a tool call blocks for a receipt.
	Wait time.Duration
}

// ParseLocalAgentDevices reads "id:token,id2:token2" into a map.
func ParseLocalAgentDevices(raw string) map[string]string {
	devices := map[string]string{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		id, token, ok := strings.Cut(entry, ":")
		if !ok || strings.TrimSpace(id) == "" || strings.TrimSpace(token) == "" {
			continue
		}
		devices[strings.TrimSpace(id)] = strings.TrimSpace(token)
	}
	return devices
}

// localPending is one task waiting for an execution endpoint.
type localPending struct {
	task    localagent.Task
	device  string
	waiting chan localagent.Receipt
	// expiresAt is when the host stops handing this task out. An endpoint that
	// claims a task and dies must not leave it queued forever.
	expiresAt time.Time
}

// localQueue holds tasks between the model and an execution endpoint.
//
// It is in-memory on purpose: a task is only meaningful while a tool call is
// blocked on it, so persisting the queue would restore work nobody is waiting
// for. Receipts are kept just long enough for a retrying endpoint to fetch one.
type localQueue struct {
	mu       sync.Mutex
	pending  map[string][]*localPending
	inflight map[string]*localPending
	receipts map[string]localagent.Receipt
	order    []string
}

func newLocalQueue() *localQueue {
	return &localQueue{
		pending:  map[string][]*localPending{},
		inflight: map[string]*localPending{},
		receipts: map[string]localagent.Receipt{},
	}
}

const localReceiptKeep = 32

func (q *localQueue) enqueue(task localagent.Task, device string, ttl time.Duration) *localPending {
	item := &localPending{task: task, device: device, waiting: make(chan localagent.Receipt, 1), expiresAt: time.Now().Add(ttl)}
	q.mu.Lock()
	q.pending[device] = append(q.pending[device], item)
	q.mu.Unlock()
	return item
}

func (q *localQueue) claim(device string) *localagent.Task {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	queued := q.pending[device]
	for len(queued) > 0 {
		item := queued[0]
		queued = queued[1:]
		if now.After(item.expiresAt) {
			// Nobody is waiting any more; drop it rather than hand it out.
			item.waiting <- localagent.Receipt{Protocol: localagent.Protocol, OperationID: item.task.OperationID, Status: "failed", Error: "local task expired before a device claimed it"}
			continue
		}
		q.pending[device] = queued
		q.inflight[item.task.OperationID] = item
		task := item.task
		return &task
	}
	q.pending[device] = queued
	return nil
}

func (q *localQueue) complete(receipt localagent.Receipt) (*localPending, bool) {
	q.mu.Lock()
	item, ok := q.inflight[receipt.OperationID]
	if ok {
		delete(q.inflight, receipt.OperationID)
	}
	q.receipts[receipt.OperationID] = receipt
	q.order = append(q.order, receipt.OperationID)
	for len(q.order) > localReceiptKeep {
		delete(q.receipts, q.order[0])
		q.order = q.order[1:]
	}
	q.mu.Unlock()
	if ok {
		item.waiting <- receipt
	}
	return item, ok
}

// cancel fails a queued task whose caller stopped waiting.
func (q *localQueue) cancel(operationID string, reason string) {
	q.mu.Lock()
	var found *localPending
	for device, queued := range q.pending {
		for index, item := range queued {
			if item.task.OperationID == operationID {
				q.pending[device] = append(append([]*localPending(nil), queued[:index]...), queued[index+1:]...)
				found = item
				break
			}
		}
	}
	q.mu.Unlock()
	_ = found
	_ = reason
}

// localAgentTools exposes the configured execution endpoint to the model.
func (s *Server) localAgentTools() []probe.Tool {
	devices := s.config.LocalAgent.Devices
	if len(devices) == 0 {
		return nil
	}
	names := make([]string, 0, len(devices))
	for id := range devices {
		names = append(names, id)
	}
	sort.Strings(names)
	return []probe.Tool{{
		Name:    "local_workspace",
		Order:   780,
		Timeout: s.localAgentWait() + 5*time.Second,
		Usage: "when the user mentions a file, folder or program on their own computer — 我的电脑, 本机, 桌面, 本地, \"on my machine\" — try this before answering that you have no access, because a paired machine may be online. " +
			"A refusal here is a fact (the endpoint was off); guessing that no channel exists is not. If the call times out, say the endpoint is not running.",
		Description: "Read, list or search files in the workspace of a paired computer, or run one of its allow-listed actions. " +
			"Paths are relative to that workspace; anything escaping it is refused. Actions are fixed argv entries the owner configured — you choose a name, never a command line.",
		Parameters: json.RawMessage(`{"type":"object","properties":{` +
			`"kind":{"type":"string","enum":["list","read","search","exec"],"description":"what to do"},` +
			`"path":{"type":"string","description":"workspace-relative path for list or read"},` +
			`"query":{"type":"string","description":"substring for search"},` +
			`"action":{"type":"string","description":"allow-list action name for exec"}` +
			`},"required":["kind"],"additionalProperties":false}`),
		Execute: s.runLocalTask,
	}}
}

func (s *Server) localAgentWait() time.Duration {
	if s.config.LocalAgent.Wait > 0 {
		return s.config.LocalAgent.Wait
	}
	return 60 * time.Second
}

func (s *Server) runLocalTask(ctx context.Context, raw json.RawMessage) (probe.ToolResult, error) {
	var input struct {
		Kind   string `json:"kind"`
		Path   string `json:"path"`
		Query  string `json:"query"`
		Action string `json:"action"`
	}
	if json.Unmarshal(raw, &input) != nil {
		return probe.ToolResult{}, errors.New("invalid request")
	}
	device := s.config.LocalAgent.DefaultDevice
	if device == "" {
		for id := range s.config.LocalAgent.Devices {
			device = id
			break
		}
	}
	operationID, err := newOperationID()
	if err != nil {
		return probe.ToolResult{}, err
	}
	task := localagent.Task{Protocol: localagent.Protocol, OperationID: operationID, Kind: input.Kind, Path: input.Path, Query: input.Query, Action: input.Action}
	item := s.localQueue.enqueue(task, device, s.localAgentWait()+10*time.Second)
	defer s.localQueue.cancel(operationID, "caller stopped waiting")

	waited := s.localAgentWait()
	select {
	case receipt := <-item.waiting:
		return localReceiptResult(receipt)
	case <-ctx.Done():
		return probe.ToolResult{}, errors.New("the local endpoint did not answer in time; it may be offline")
	case <-time.After(waited):
		return probe.ToolResult{}, fmt.Errorf("the local endpoint %q did not claim the task within %s; it may be offline", device, waited)
	}
}

// localReceiptResult turns an endpoint receipt into a tool result.
func localReceiptResult(receipt localagent.Receipt) (probe.ToolResult, error) {
	result, err := encoded(map[string]any{
		"status":    receipt.Status,
		"output":    receipt.Output,
		"entries":   receipt.Entries,
		"artifacts": receipt.Artifacts,
		"error":     receipt.Error,
		"guard":     receipt.Guard,
	})
	if err != nil {
		return probe.ToolResult{}, err
	}
	if receipt.Status != "completed" {
		result.Output = fmt.Sprintf("本机执行未成功（%s）：%s", receipt.Status, receipt.Error)
	}
	return result, nil
}

func newOperationID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "op_" + hex.EncodeToString(b[:]), nil
}

// localAgentAuthorized resolves a bearer token to a configured device id.
func (s *Server) localAgentAuthorized(r *http.Request) (string, bool) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	token = strings.TrimSpace(token)
	if token == "" {
		return "", false
	}
	device := strings.TrimSpace(r.Header.Get("X-Chat-Agent-ID"))
	expected, ok := s.config.LocalAgent.Devices[device]
	if !ok {
		return "", false
	}
	if subtle.ConstantTimeCompare([]byte(expected), []byte(token)) != 1 {
		return "", false
	}
	return device, true
}

func (s *Server) localAgentClaim(w http.ResponseWriter, r *http.Request) {
	device, ok := s.localAgentAuthorized(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unknown device or bad token"})
		return
	}
	var input struct {
		Protocol     string   `json:"protocol"`
		DeviceID     string   `json:"device_id"`
		Capabilities []string `json:"capabilities"`
	}
	if decodeInput(w, r, &input) && input.DeviceID != "" && input.DeviceID != device {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "device id mismatch"})
		return
	}
	task := s.localQueue.claim(device)
	if task == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) localAgentComplete(w http.ResponseWriter, r *http.Request) {
	device, ok := s.localAgentAuthorized(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unknown device or bad token"})
		return
	}
	operationID := r.PathValue("operation")
	var input struct {
		DeviceID string             `json:"device_id"`
		Receipt  localagent.Receipt `json:"receipt"`
	}
	if !decodeInput(w, r, &input) {
		return
	}
	if input.DeviceID != "" && input.DeviceID != device {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "device id mismatch"})
		return
	}
	receipt := input.Receipt
	if receipt.OperationID == "" {
		receipt.OperationID = operationID
	}
	if receipt.OperationID != operationID {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "receipt operation id does not match the path"})
		return
	}
	if _, found := s.localQueue.complete(receipt); !found {
		// A retry after the caller gave up is expected; accept it quietly so the
		// endpoint can drain its outbox.
		log.Printf("local agent receipt for unknown operation %s", operationID)
	}
	w.WriteHeader(http.StatusNoContent)
}
