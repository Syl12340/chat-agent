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
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"chat-agent/internal/localagent"
	"chat-agent/internal/probe"
	"chat-agent/internal/storage"
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
	expiresAt    time.Time
	claimedAt    *time.Time
	canceled     bool
	cancelReason string
}

type localDeviceState struct {
	DeviceID     string    `json:"device_id"`
	Configured   bool      `json:"configured"`
	Online       bool      `json:"online"`
	LastSeen     time.Time `json:"last_seen,omitempty"`
	Capabilities []string  `json:"capabilities,omitempty"`
}

type localOperationView struct {
	OperationID string     `json:"operation_id"`
	DeviceID    string     `json:"device_id"`
	Kind        string     `json:"kind"`
	Target      string     `json:"target,omitempty"`
	Status      string     `json:"status"`
	Error       string     `json:"error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ClaimedAt   *time.Time `json:"claimed_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// localQueue holds tasks between the model and an execution endpoint.
//
// Live tasks stay in memory: restoring them would replay work nobody is waiting
// for. A bounded operation index is persisted for status/history only; unfinished
// entries become interrupted after restart and are never handed out again.
type localQueue struct {
	mu         sync.Mutex
	pending    map[string][]*localPending
	inflight   map[string]*localPending
	receipts   map[string]localagent.Receipt
	order      []string
	devices    map[string]localDeviceState
	operations map[string]localOperationView
	canceled   map[string]bool
	statePath  string
}

func newLocalQueue(paths ...string) *localQueue {
	q := &localQueue{
		pending:    map[string][]*localPending{},
		inflight:   map[string]*localPending{},
		receipts:   map[string]localagent.Receipt{},
		devices:    map[string]localDeviceState{},
		operations: map[string]localOperationView{},
		canceled:   map[string]bool{},
	}
	if len(paths) > 0 && paths[0] != "" {
		q.statePath = paths[0]
		_ = os.MkdirAll(filepath.Dir(q.statePath), 0700)
		var saved []localOperationView
		if raw, err := os.ReadFile(q.statePath); err == nil && json.Unmarshal(raw, &saved) == nil {
			now := time.Now().UTC()
			for _, view := range saved {
				if view.Status == "queued" || view.Status == "running" {
					view.Status, view.Error, view.CompletedAt = "interrupted", "服务重启中断；任务不会自动重放", &now
				}
				q.operations[view.OperationID] = view
			}
			q.saveLocked()
		}
	}
	return q
}

func (q *localQueue) saveLocked() {
	if q.statePath == "" {
		return
	}
	items := make([]localOperationView, 0, len(q.operations))
	for _, item := range q.operations {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	if len(items) > 200 {
		items = items[:200]
	}
	if raw, err := json.Marshal(items); err == nil {
		_ = storage.Replace(q.statePath, raw)
	}
}

const localReceiptKeep = 32

func (q *localQueue) enqueue(task localagent.Task, device string, ttl time.Duration) *localPending {
	item := &localPending{task: task, device: device, waiting: make(chan localagent.Receipt, 1), expiresAt: time.Now().Add(ttl)}
	q.mu.Lock()
	q.pending[device] = append(q.pending[device], item)
	target := task.Path
	if target == "" {
		target = task.Action
	}
	q.operations[task.OperationID] = localOperationView{OperationID: task.OperationID, DeviceID: device, Kind: task.Kind, Target: target, Status: "queued", CreatedAt: time.Now().UTC()}
	q.saveLocked()
	q.mu.Unlock()
	return item
}

func (q *localQueue) claim(device string, capabilities []string) *localagent.Task {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	q.devices[device] = localDeviceState{DeviceID: device, Configured: true, Online: true, LastSeen: now.UTC(), Capabilities: append([]string(nil), capabilities...)}
	queued := q.pending[device]
	for len(queued) > 0 {
		item := queued[0]
		queued = queued[1:]
		if now.After(item.expiresAt) {
			// Nobody is waiting any more; drop it rather than hand it out.
			item.waiting <- localagent.Receipt{Protocol: localagent.Protocol, OperationID: item.task.OperationID, Status: "failed", Error: "local task expired before a device claimed it"}
			completed := now.UTC()
			view := q.operations[item.task.OperationID]
			view.Status, view.Error, view.CompletedAt = "failed", "local task expired before a device claimed it", &completed
			q.operations[item.task.OperationID] = view
			q.saveLocked()
			continue
		}
		q.pending[device] = queued
		q.inflight[item.task.OperationID] = item
		claimed := now.UTC()
		item.claimedAt = &claimed
		view := q.operations[item.task.OperationID]
		view.Status, view.ClaimedAt = "running", &claimed
		q.operations[item.task.OperationID] = view
		q.saveLocked()
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
	view := q.operations[receipt.OperationID]
	if view.Status != "canceled" {
		q.receipts[receipt.OperationID] = receipt
		view.Status, view.Error = receipt.Status, receipt.Error
		completed := receipt.CompletedAt
		if completed.IsZero() {
			completed = time.Now().UTC()
		}
		view.CompletedAt = &completed
		q.operations[receipt.OperationID] = view
	}
	q.order = append(q.order, receipt.OperationID)
	for len(q.order) > localReceiptKeep {
		delete(q.receipts, q.order[0])
		q.order = q.order[1:]
	}
	q.saveLocked()
	q.mu.Unlock()
	if ok {
		item.waiting <- receipt
	}
	return item, ok
}

// cancel fails a queued task whose caller stopped waiting.
func (q *localQueue) cancel(operationID string, reason string) bool {
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
	if item := q.inflight[operationID]; item != nil {
		found = item
		item.canceled, item.cancelReason = true, reason
		delete(q.inflight, operationID)
	}
	if found != nil {
		q.canceled[operationID] = true
		now := time.Now().UTC()
		view := q.operations[operationID]
		view.Status, view.Error, view.CompletedAt = "canceled", reason, &now
		q.operations[operationID] = view
		q.saveLocked()
	}
	q.mu.Unlock()
	if found != nil {
		select {
		case found.waiting <- localagent.Receipt{Protocol: localagent.Protocol, OperationID: operationID, Status: "canceled", Error: reason, CompletedAt: time.Now().UTC()}:
		default:
		}
	}
	return found != nil
}

func (q *localQueue) cancelRequested(operationID string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.canceled[operationID]
}

func (q *localQueue) snapshot(configured map[string]string) ([]localDeviceState, []localOperationView) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	devices := make([]localDeviceState, 0, len(configured))
	for id := range configured {
		state := q.devices[id]
		state.DeviceID, state.Configured = id, true
		state.Online = !state.LastSeen.IsZero() && now.Sub(state.LastSeen) < 45*time.Second
		devices = append(devices, state)
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].DeviceID < devices[j].DeviceID })
	operations := make([]localOperationView, 0, len(q.operations))
	for _, item := range q.operations {
		operations = append(operations, item)
	}
	sort.Slice(operations, func(i, j int) bool { return operations[i].CreatedAt.After(operations[j].CreatedAt) })
	if len(operations) > 50 {
		operations = operations[:50]
	}
	return devices, operations
}

func (q *localQueue) artifactUploadAllowed(operationID, device string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	item := q.inflight[operationID]
	return item != nil && item.device == device && !q.canceled[operationID]
}

func (q *localQueue) artifact(operationID, digest string) (localagent.Artifact, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	receipt, ok := q.receipts[operationID]
	if !ok {
		return localagent.Artifact{}, false
	}
	for _, item := range receipt.Artifacts {
		if item.SHA256 == digest {
			return item, true
		}
	}
	return localagent.Artifact{}, false
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
	read := probe.Tool{
		Name:    "local_workspace",
		Kind:    "read",
		Order:   780,
		Timeout: s.localAgentWait() + 5*time.Second,
		Usage: "when the user mentions a file, folder or program on their own computer — 我的电脑, 本机, 桌面, 本地, \"on my machine\" — try this before answering that you have no access, because a paired machine may be online. " +
			"A refusal here is a fact (the endpoint was off); guessing that no channel exists is not. If the call times out, say the endpoint is not running.",
		Description: "Read, list or search files in the workspace of a paired computer. Paths are relative to that workspace; anything escaping it is refused.",
		Parameters: json.RawMessage(`{"type":"object","properties":{` +
			`"kind":{"type":"string","enum":["list","read","search"],"description":"what to do"},` +
			`"path":{"type":"string","description":"workspace-relative path for list or read"},` +
			`"query":{"type":"string","description":"substring for search"}` +
			`},"required":["kind"],"additionalProperties":false}`),
		Present: func(raw json.RawMessage) (string, string) {
			var v struct{ Path, Query string }
			_ = json.Unmarshal(raw, &v)
			if v.Path != "" {
				return "读取本机", v.Path
			}
			return "搜索本机", v.Query
		},
		Execute: s.runLocalTask,
	}
	write := probe.Tool{Name: "local_workspace_write", Kind: "edit", Order: 781, Timeout: s.localAgentWait() + 5*time.Second,
		Description: "Create or replace one bounded UTF-8 file in the paired computer workspace. The expected hash makes the write fail if the file changed; use missing only for a new file.",
		Usage:       "read the file first, then request the smallest exact edit. This always requires explicit user approval.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string","maxLength":262144},"expected_sha256":{"type":"string","description":"current lowercase SHA-256 or missing"}},"required":["path","content","expected_sha256"],"additionalProperties":false}`),
		Decision:    probe.DecisionRequired, Risk: probe.RiskHigh, ApprovalRequired: true,
		Present: func(raw json.RawMessage) (string, string) {
			var v struct{ Path string }
			_ = json.Unmarshal(raw, &v)
			return "写入本机", v.Path
		},
		Execute: s.runLocalWrite,
	}
	action := probe.Tool{Name: "local_action", Kind: "execute", Order: 782, Timeout: s.localAgentWait() + 5*time.Second,
		Description: "Run one fixed action configured by the owner on the paired computer. This never accepts a shell command or arguments.",
		Usage:       "use only for an action the user explicitly requested. This always requires explicit user approval.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"action":{"type":"string"}},"required":["action"],"additionalProperties":false}`),
		Decision:    probe.DecisionRequired, Risk: probe.RiskHigh, ApprovalRequired: true,
		Present: func(raw json.RawMessage) (string, string) {
			var v struct{ Action string }
			_ = json.Unmarshal(raw, &v)
			return "本机动作", v.Action
		},
		Execute: s.runLocalAction,
	}
	share := probe.Tool{Name: "local_file_share", Kind: "read", Order: 783, Timeout: s.localAgentWait() + 5*time.Second,
		Description: "Upload one existing file from the paired workspace to the server and offer it to the user as an authenticated download.", Usage: "use only when the user asks to receive that exact local file. This always requires explicit approval because file contents leave the computer.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`), Decision: probe.DecisionRequired, Risk: probe.RiskHigh, ApprovalRequired: true,
		Present: func(raw json.RawMessage) (string, string) {
			var v struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal(raw, &v)
			return "发送本机文件", v.Path
		}, Execute: s.runLocalShare}
	return []probe.Tool{read, write, action, share}
}

func (s *Server) localAgentWait() time.Duration {
	if s.config.LocalAgent.Wait > 0 {
		return s.config.LocalAgent.Wait
	}
	return 60 * time.Second
}

func (s *Server) runLocalTask(ctx context.Context, raw json.RawMessage) (probe.ToolResult, error) {
	var input struct {
		Kind  string `json:"kind"`
		Path  string `json:"path"`
		Query string `json:"query"`
	}
	if json.Unmarshal(raw, &input) != nil {
		return probe.ToolResult{}, errors.New("invalid request")
	}
	return s.dispatchLocalTask(ctx, localagent.Task{Kind: input.Kind, Path: input.Path, Query: input.Query})
}

func (s *Server) runLocalWrite(ctx context.Context, raw json.RawMessage) (probe.ToolResult, error) {
	var input struct {
		Path           string `json:"path"`
		Content        string `json:"content"`
		ExpectedSHA256 string `json:"expected_sha256"`
	}
	if json.Unmarshal(raw, &input) != nil {
		return probe.ToolResult{}, errors.New("invalid request")
	}
	return s.dispatchLocalTask(ctx, localagent.Task{Kind: "write", Path: input.Path, Content: input.Content, ExpectedSHA256: input.ExpectedSHA256})
}

func (s *Server) runLocalAction(ctx context.Context, raw json.RawMessage) (probe.ToolResult, error) {
	var input struct {
		Action string `json:"action"`
	}
	if json.Unmarshal(raw, &input) != nil {
		return probe.ToolResult{}, errors.New("invalid request")
	}
	return s.dispatchLocalTask(ctx, localagent.Task{Kind: "exec", Action: input.Action})
}

func (s *Server) runLocalShare(ctx context.Context, raw json.RawMessage) (probe.ToolResult, error) {
	var input struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(raw, &input) != nil {
		return probe.ToolResult{}, errors.New("invalid request")
	}
	return s.dispatchLocalTask(ctx, localagent.Task{Kind: "share", Artifacts: []string{input.Path}})
}

func (s *Server) dispatchLocalTask(ctx context.Context, task localagent.Task) (probe.ToolResult, error) {
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
	task.Protocol, task.OperationID = localagent.Protocol, operationID
	item := s.localQueue.enqueue(task, device, s.localAgentWait()+10*time.Second)
	defer s.localQueue.cancel(operationID, "caller stopped waiting")
	select {
	case receipt := <-item.waiting:
		return localReceiptResult(receipt)
	case <-ctx.Done():
		return probe.ToolResult{}, errors.New("the local endpoint did not answer in time; it may be offline")
	case <-time.After(s.localAgentWait()):
		return probe.ToolResult{}, fmt.Errorf("the local endpoint %q did not claim the task within %s; it may be offline", device, s.localAgentWait())
	}
}

// localReceiptResult turns an endpoint receipt into a tool result.
func localReceiptResult(receipt localagent.Receipt) (probe.ToolResult, error) {
	artifacts := make([]map[string]any, 0, len(receipt.Artifacts))
	for _, item := range receipt.Artifacts {
		artifacts = append(artifacts, map[string]any{"name": item.Name, "media_type": item.MediaType, "bytes": item.Bytes, "sha256": item.SHA256, "download_url": "/api/local-agent/artifacts/" + receipt.OperationID + "/" + item.SHA256})
	}
	result, err := encoded(map[string]any{
		"status":    receipt.Status,
		"output":    receipt.Output,
		"entries":   receipt.Entries,
		"artifacts": artifacts,
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
	if !decodeInput(w, r, &input) {
		return
	}
	if input.DeviceID != "" && input.DeviceID != device {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "device id mismatch"})
		return
	}
	task := s.localQueue.claim(device, input.Capabilities)
	if task == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) localAgentCancelState(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.localAgentAuthorized(r); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unknown device or bad token"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"cancel_requested": s.localQueue.cancelRequested(r.PathValue("operation"))})
}

func (s *Server) localAgentStatus(w http.ResponseWriter, r *http.Request) {
	devices, operations := s.localQueue.snapshot(s.config.LocalAgent.Devices)
	writeJSON(w, http.StatusOK, map[string]any{"configured": len(s.config.LocalAgent.Devices) > 0, "default_device": s.config.LocalAgent.DefaultDevice, "devices": devices, "operations": operations})
}

func (s *Server) cancelLocalOperation(w http.ResponseWriter, r *http.Request) {
	if !s.localQueue.cancel(r.PathValue("operation"), "用户从本机工作台取消") {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "任务已结束或不存在"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
