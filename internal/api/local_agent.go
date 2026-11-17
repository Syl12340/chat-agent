package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
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
	Actions      []string  `json:"actions,omitempty"`
}

type localOperationFile struct {
	Name        string `json:"name"`
	MediaType   string `json:"media_type"`
	Bytes       int64  `json:"bytes"`
	SHA256      string `json:"sha256"`
	DownloadURL string `json:"download_url"`
}

type localOperationView struct {
	OperationID string               `json:"operation_id"`
	DeviceID    string               `json:"device_id"`
	RequestSHA  string               `json:"request_sha256,omitempty"`
	Kind        string               `json:"kind"`
	Target      string               `json:"target,omitempty"`
	Status      string               `json:"status"`
	Output      string               `json:"output,omitempty"`
	Error       string               `json:"error,omitempty"`
	Artifacts   int                  `json:"artifacts,omitempty"`
	Files       []localOperationFile `json:"files,omitempty"`
	CreatedAt   time.Time            `json:"created_at"`
	ClaimedAt   *time.Time           `json:"claimed_at,omitempty"`
	CompletedAt *time.Time           `json:"completed_at,omitempty"`
	RetainUntil time.Time            `json:"retain_until"`
}

type localQueueState struct {
	Version    int                  `json:"version"`
	Operations []localOperationView `json:"operations"`
	Tasks      []localagent.Task    `json:"tasks,omitempty"`
	Receipts   []localagent.Receipt `json:"receipts,omitempty"`
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
	tasks      map[string]localagent.Task
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
		tasks:      map[string]localagent.Task{},
		canceled:   map[string]bool{},
	}
	if len(paths) > 0 && paths[0] != "" {
		q.statePath = paths[0]
		_ = os.MkdirAll(filepath.Dir(q.statePath), 0700)
		var saved []localOperationView
		var tasks []localagent.Task
		var receipts []localagent.Receipt
		if raw, err := os.ReadFile(q.statePath); err == nil {
			var state localQueueState
			if json.Unmarshal(raw, &state) == nil && (state.Version == 2 || state.Version == 3) {
				saved, receipts = state.Operations, state.Receipts
				if state.Version == 3 {
					tasks = state.Tasks
				}
			} else {
				_ = json.Unmarshal(raw, &saved)
			}
		}
		if len(saved) > 0 || len(receipts) > 0 {
			now := time.Now().UTC()
			for _, view := range saved {
				if view.RetainUntil.IsZero() {
					view.RetainUntil = view.CreatedAt.Add(localOperationRetention)
				}
				if view.Status != "queued" && view.Status != "running" && view.Status != "cancel_requested" && now.After(view.RetainUntil) {
					q.removeArtifactDir(view.OperationID)
					continue
				}
				if view.Status == "queued" || view.Status == "running" || view.Status == "cancel_requested" {
					view.Status, view.Error, view.CompletedAt = "interrupted", "服务重启中断；任务不会自动重放", &now
					view.RetainUntil = now.Add(localOperationRetention)
				}
				q.operations[view.OperationID] = view
			}
			for _, task := range tasks {
				view, ok := q.operations[task.OperationID]
				if !ok || task.Protocol != localagent.Protocol || localTaskDigest(task) != view.RequestSHA {
					continue
				}
				q.tasks[task.OperationID] = task
			}
			sort.Slice(receipts, func(i, j int) bool { return receipts[i].CompletedAt.Before(receipts[j].CompletedAt) })
			if len(receipts) > localReceiptKeep {
				receipts = receipts[len(receipts)-localReceiptKeep:]
			}
			for _, receipt := range receipts {
				if receipt.OperationID == "" {
					continue
				}
				q.receipts[receipt.OperationID] = receipt
				q.order = append(q.order, receipt.OperationID)
				if view, ok := q.operations[receipt.OperationID]; ok {
					view.Files = localOperationFiles(receipt)
					view.Artifacts = len(view.Files)
					q.operations[receipt.OperationID] = view
				}
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
	if len(items) > localOperationKeep {
		items = items[:localOperationKeep]
	}
	tasks := make([]localagent.Task, 0, len(items))
	for _, item := range items {
		if task, ok := q.tasks[item.OperationID]; ok {
			tasks = append(tasks, task)
		}
	}
	receipts := make([]localagent.Receipt, 0, len(q.order))
	for _, id := range q.order {
		if receipt, ok := q.receipts[id]; ok {
			receipts = append(receipts, receipt)
		}
	}
	if raw, err := json.Marshal(localQueueState{Version: 3, Operations: items, Tasks: tasks, Receipts: receipts}); err == nil {
		if err := storage.Replace(q.statePath, raw); err != nil {
			log.Printf("local agent operation state could not be saved")
		}
	}
}

const (
	localOperationKeep      = 512
	localReceiptKeep        = 128
	localOperationRetention = 7 * 24 * time.Hour
)

func (q *localQueue) removeArtifactDir(operationID string) {
	if q.statePath == "" || !storage.ValidID(operationID) || !strings.HasPrefix(operationID, "op_") {
		return
	}
	_ = os.RemoveAll(filepath.Join(filepath.Dir(q.statePath), "artifacts", operationID))
}

func (q *localQueue) pruneExpiredLocked(now time.Time) {
	changed := false
	for id, view := range q.operations {
		if view.Status == "queued" || view.Status == "running" || view.Status == "cancel_requested" || view.RetainUntil.IsZero() || !now.After(view.RetainUntil) {
			continue
		}
		delete(q.operations, id)
		delete(q.tasks, id)
		delete(q.receipts, id)
		delete(q.canceled, id)
		q.removeArtifactDir(id)
		changed = true
	}
	if changed {
		kept := q.order[:0]
		for _, id := range q.order {
			if _, ok := q.receipts[id]; ok {
				kept = append(kept, id)
			}
		}
		q.order = kept
		q.saveLocked()
	}
}

func (q *localQueue) enqueue(task localagent.Task, device string, ttl time.Duration) *localPending {
	item := &localPending{task: task, device: device, waiting: make(chan localagent.Receipt, 1), expiresAt: time.Now().Add(ttl)}
	q.mu.Lock()
	q.pending[device] = append(q.pending[device], item)
	target := task.Path
	if target == "" {
		target = task.Action
	}
	now := time.Now().UTC()
	q.operations[task.OperationID] = localOperationView{OperationID: task.OperationID, DeviceID: device, RequestSHA: localTaskDigest(task), Kind: task.Kind, Target: target, Status: "queued", CreatedAt: now, RetainUntil: now.Add(localOperationRetention)}
	q.tasks[task.OperationID] = task
	q.saveLocked()
	q.mu.Unlock()
	return item
}

func (q *localQueue) claim(device string, capabilities, actions []string) *localagent.Task {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	q.devices[device] = localDeviceState{DeviceID: device, Configured: true, Online: true, LastSeen: now.UTC(), Capabilities: append([]string(nil), capabilities...), Actions: append([]string(nil), actions...)}
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

func localTaskDigest(task localagent.Task) string {
	raw, _ := json.Marshal(task)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func localOperationFiles(receipt localagent.Receipt) []localOperationFile {
	files := make([]localOperationFile, 0, len(receipt.Artifacts))
	for _, item := range receipt.Artifacts {
		if !validLocalArtifact(item) {
			continue
		}
		files = append(files, localOperationFile{
			Name: item.Name, MediaType: item.MediaType, Bytes: item.Bytes, SHA256: item.SHA256,
			DownloadURL: "/api/local-agent/artifacts/" + receipt.OperationID + "/" + item.SHA256,
		})
	}
	return files
}

func boundedLocalOutput(value string) string {
	const limit = 32 << 10
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "\n[输出已截断；完整结果应作为文件产物返回]"
}

func validLocalArtifact(item localagent.Artifact) bool {
	if item.Name == "" || len(item.Name) > 512 || strings.ContainsAny(item.Name, "\r\n\x00") || item.Bytes < 0 || item.Bytes > localArtifactLimit || len(item.SHA256) != 64 || len(item.MediaType) > 128 || strings.ContainsAny(item.MediaType, "\r\n\x00") {
		return false
	}
	_, err := hex.DecodeString(item.SHA256)
	return err == nil && strings.ToLower(item.SHA256) == item.SHA256
}

func (q *localQueue) complete(device string, receipt localagent.Receipt) (*localPending, bool, error) {
	if len(receipt.Artifacts) > 16 {
		return nil, false, errors.New("receipt contains too many artifacts")
	}
	for _, artifact := range receipt.Artifacts {
		if !validLocalArtifact(artifact) {
			return nil, false, errors.New("receipt contains invalid artifact metadata")
		}
	}
	q.mu.Lock()
	item, ok := q.inflight[receipt.OperationID]
	if !ok {
		if prior, found := q.receipts[receipt.OperationID]; found && reflect.DeepEqual(prior, receipt) {
			q.mu.Unlock()
			return nil, false, nil
		}
		view, found := q.operations[receipt.OperationID]
		storedTask, taskStored := q.tasks[receipt.OperationID]
		if found && view.DeviceID == device && view.ClaimedAt != nil && view.Status == "interrupted" && receipt.Protocol == localagent.Protocol && receipt.RequestSHA256 == view.RequestSHA && (!taskStored || localTaskDigest(storedTask) == view.RequestSHA) {
			switch receipt.Status {
			case "completed", "failed", "rejected", "canceled":
				q.receipts[receipt.OperationID] = receipt
				q.order = append(q.order, receipt.OperationID)
				for len(q.order) > localReceiptKeep {
					delete(q.receipts, q.order[0])
					q.order = q.order[1:]
				}
				view.Files = localOperationFiles(receipt)
				view.Status, view.Output, view.Error, view.Artifacts = receipt.Status, boundedLocalOutput(receipt.Output), receipt.Error, len(view.Files)
				completed := receipt.CompletedAt
				if completed.IsZero() {
					completed = time.Now().UTC()
				}
				view.CompletedAt = &completed
				q.operations[receipt.OperationID] = view
				q.saveLocked()
				q.mu.Unlock()
				return nil, false, nil
			}
		}
		q.mu.Unlock()
		return nil, false, errors.New("operation is not running")
	}
	if item.device != device {
		q.mu.Unlock()
		return nil, false, errors.New("operation belongs to another device")
	}
	if receipt.Protocol != localagent.Protocol || receipt.OperationID != item.task.OperationID || receipt.RequestSHA256 != localTaskDigest(item.task) {
		q.mu.Unlock()
		return nil, false, errors.New("receipt does not match the claimed task")
	}
	switch receipt.Status {
	case "completed", "failed", "rejected", "canceled":
	default:
		q.mu.Unlock()
		return nil, false, errors.New("receipt status is invalid")
	}
	delete(q.inflight, receipt.OperationID)
	delete(q.canceled, receipt.OperationID)
	view := q.operations[receipt.OperationID]
	if view.Status != "canceled" {
		q.receipts[receipt.OperationID] = receipt
		view.Files = localOperationFiles(receipt)
		view.Status, view.Output, view.Error, view.Artifacts = receipt.Status, boundedLocalOutput(receipt.Output), receipt.Error, len(view.Files)
		completed := receipt.CompletedAt
		if completed.IsZero() {
			completed = time.Now().UTC()
		}
		view.CompletedAt = &completed
		view.RetainUntil = completed.Add(localOperationRetention)
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
	return item, true, nil
}

// cancel removes a queued task immediately, but a running task only enters
// cancel_requested. The endpoint owns the terminal fact: it may report canceled,
// completed or failed after observing the request, and the host records that
// receipt instead of pretending a request rolled work back.
func (q *localQueue) cancel(operationID string, reason string) bool {
	q.mu.Lock()
	var found *localPending
	queuedCancellation := false
	for device, queued := range q.pending {
		for index, item := range queued {
			if item.task.OperationID == operationID {
				q.pending[device] = append(append([]*localPending(nil), queued[:index]...), queued[index+1:]...)
				found = item
				queuedCancellation = true
				break
			}
		}
	}
	if item := q.inflight[operationID]; item != nil {
		found = item
		item.canceled, item.cancelReason = true, reason
	}
	if found != nil {
		q.canceled[operationID] = true
		view := q.operations[operationID]
		view.Error = reason
		if queuedCancellation {
			now := time.Now().UTC()
			view.Status, view.CompletedAt = "canceled", &now
			view.RetainUntil = now.Add(localOperationRetention)
		} else {
			view.Status, view.CompletedAt = "cancel_requested", nil
		}
		q.operations[operationID] = view
		q.saveLocked()
	}
	q.mu.Unlock()
	if found != nil && queuedCancellation {
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

func (q *localQueue) deviceOwns(operationID, device string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	view, ok := q.operations[operationID]
	return ok && view.DeviceID == device
}

func (q *localQueue) snapshot(configured map[string]string) ([]localDeviceState, []localOperationView) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	q.pruneExpiredLocked(now)
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
	if view, ok := q.operations[operationID]; !ok || view.RetainUntil.IsZero() || time.Now().After(view.RetainUntil) {
		return localagent.Artifact{}, false
	}
	if receipt, ok := q.receipts[operationID]; ok {
		for _, item := range receipt.Artifacts {
			if item.SHA256 == digest && validLocalArtifact(item) {
				return item, true
			}
		}
	}
	if view, found := q.operations[operationID]; found {
		for _, file := range view.Files {
			item := localagent.Artifact{Name: file.Name, MediaType: file.MediaType, Bytes: file.Bytes, SHA256: file.SHA256}
			if item.SHA256 == digest && validLocalArtifact(item) {
				return item, true
			}
		}
	}
	return localagent.Artifact{}, false
}

func (q *localQueue) scopedOperation(operationID, sessionID, roleID string) (localOperationView, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	view, ok := q.operations[operationID]
	task, taskOK := q.tasks[operationID]
	if !ok || !taskOK || task.SessionID != sessionID || task.RoleID != roleID {
		return localOperationView{}, false
	}
	return view, true
}

type localTaskScope struct {
	SessionID string
	RoleID    string
	RequestID string
}

func (scope localTaskScope) bind(task localagent.Task) localagent.Task {
	task.SessionID, task.RoleID, task.RequestID = scope.SessionID, scope.RoleID, scope.RequestID
	return task
}

// localAgentTools exposes the configured execution endpoint to the model.
func (s *Server) localAgentTools(scope localTaskScope) []probe.Tool {
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
		Execute: func(ctx context.Context, raw json.RawMessage) (probe.ToolResult, error) {
			return s.runLocalTask(ctx, raw, scope)
		},
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
		Execute: func(ctx context.Context, raw json.RawMessage) (probe.ToolResult, error) {
			return s.runLocalWrite(ctx, raw, scope)
		},
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
		Execute: func(ctx context.Context, raw json.RawMessage) (probe.ToolResult, error) {
			return s.runLocalAction(ctx, raw, scope)
		},
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
		}, Execute: func(ctx context.Context, raw json.RawMessage) (probe.ToolResult, error) {
			return s.runLocalShare(ctx, raw, scope)
		}}
	jobStart := probe.Tool{Name: "local_job_start", Kind: "execute", Order: 784, Timeout: 5 * time.Second,
		Description: "Start one fixed owner-configured action on the paired computer without waiting for it to finish. Returns an operation_id; use local_job_status before claiming success or retrying.",
		Usage:       "use for an explicitly requested action that may take longer than one model step. Starting always requires approval. Never start the same job again merely because its result is still queued or running.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"action":{"type":"string"}},"required":["action"],"additionalProperties":false}`),
		Decision:    probe.DecisionRequired, Risk: probe.RiskHigh, ApprovalRequired: true,
		Present: func(raw json.RawMessage) (string, string) {
			var v struct {
				Action string `json:"action"`
			}
			_ = json.Unmarshal(raw, &v)
			return "启动本机任务", v.Action
		},
		Execute: func(ctx context.Context, raw json.RawMessage) (probe.ToolResult, error) {
			return s.runLocalJobStart(ctx, raw, scope)
		}}
	jobStatus := probe.Tool{Name: "local_job_status", Kind: "read", Order: 785, Timeout: 3 * time.Second,
		Description: "Query a previously started local job by operation_id. Only jobs from the current conversation and role are visible.",
		Usage:       "query before retrying an uncertain or long-running local job; queued and running are not success.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"operation_id":{"type":"string"}},"required":["operation_id"],"additionalProperties":false}`),
		Present: func(raw json.RawMessage) (string, string) {
			var v struct {
				OperationID string `json:"operation_id"`
			}
			_ = json.Unmarshal(raw, &v)
			return "查看本机任务", v.OperationID
		},
		Execute: func(_ context.Context, raw json.RawMessage) (probe.ToolResult, error) {
			return s.runLocalJobStatus(raw, scope)
		}}
	endpointStatus := probe.Tool{Name: "local_endpoint_status", Kind: "read", Order: 786, Timeout: 3 * time.Second,
		Description: "List paired execution endpoints, whether each is online, its declared capabilities and owner-configured fixed action names. This does not run anything.",
		Usage:       "use before proposing a local action when the available endpoint or fixed action name is unknown.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Present: func(json.RawMessage) (string, string) {
			return "查看本机执行端", "能力与在线状态"
		},
		Execute: func(_ context.Context, _ json.RawMessage) (probe.ToolResult, error) {
			devices, _ := s.localQueue.snapshot(s.config.LocalAgent.Devices)
			return encoded(map[string]any{"devices": devices, "default_device": s.config.LocalAgent.DefaultDevice, "os_sandbox": false, "free_shell": false})
		}}
	return []probe.Tool{read, write, action, share, jobStart, jobStatus, endpointStatus}
}

func (s *Server) localAgentWait() time.Duration {
	if s.config.LocalAgent.Wait > 0 {
		return s.config.LocalAgent.Wait
	}
	return 60 * time.Second
}

func (s *Server) runLocalTask(ctx context.Context, raw json.RawMessage, scope localTaskScope) (probe.ToolResult, error) {
	var input struct {
		Kind  string `json:"kind"`
		Path  string `json:"path"`
		Query string `json:"query"`
	}
	if json.Unmarshal(raw, &input) != nil {
		return probe.ToolResult{}, errors.New("invalid request")
	}
	return s.dispatchLocalTask(ctx, scope.bind(localagent.Task{Kind: input.Kind, Path: input.Path, Query: input.Query}))
}

func (s *Server) runLocalWrite(ctx context.Context, raw json.RawMessage, scope localTaskScope) (probe.ToolResult, error) {
	var input struct {
		Path           string `json:"path"`
		Content        string `json:"content"`
		ExpectedSHA256 string `json:"expected_sha256"`
	}
	if json.Unmarshal(raw, &input) != nil {
		return probe.ToolResult{}, errors.New("invalid request")
	}
	return s.dispatchLocalTask(ctx, scope.bind(localagent.Task{Kind: "write", Path: input.Path, Content: input.Content, ExpectedSHA256: input.ExpectedSHA256}))
}

func (s *Server) runLocalAction(ctx context.Context, raw json.RawMessage, scope localTaskScope) (probe.ToolResult, error) {
	var input struct {
		Action string `json:"action"`
	}
	if json.Unmarshal(raw, &input) != nil {
		return probe.ToolResult{}, errors.New("invalid request")
	}
	return s.dispatchLocalTask(ctx, scope.bind(localagent.Task{Kind: "exec", Action: input.Action}))
}

func (s *Server) runLocalShare(ctx context.Context, raw json.RawMessage, scope localTaskScope) (probe.ToolResult, error) {
	var input struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(raw, &input) != nil {
		return probe.ToolResult{}, errors.New("invalid request")
	}
	return s.dispatchLocalTask(ctx, scope.bind(localagent.Task{Kind: "share", Artifacts: []string{input.Path}}))
}

func (s *Server) runLocalJobStart(_ context.Context, raw json.RawMessage, scope localTaskScope) (probe.ToolResult, error) {
	var input struct {
		Action string `json:"action"`
	}
	if json.Unmarshal(raw, &input) != nil || strings.TrimSpace(input.Action) == "" {
		return probe.ToolResult{}, errors.New("invalid local job request")
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
	task := scope.bind(localagent.Task{Protocol: localagent.Protocol, OperationID: operationID, Kind: "exec", Action: input.Action})
	s.localQueue.enqueue(task, device, 10*time.Minute)
	return encoded(map[string]any{"operation_id": operationID, "status": "queued", "device_id": device, "instruction": "use local_job_status to observe the terminal result; do not resubmit while queued or running"})
}

func (s *Server) runLocalJobStatus(raw json.RawMessage, scope localTaskScope) (probe.ToolResult, error) {
	var input struct {
		OperationID string `json:"operation_id"`
	}
	if json.Unmarshal(raw, &input) != nil || !strings.HasPrefix(input.OperationID, "op_") {
		return probe.ToolResult{}, errors.New("invalid local operation id")
	}
	view, ok := s.localQueue.scopedOperation(input.OperationID, scope.SessionID, scope.RoleID)
	if !ok {
		return probe.ToolResult{}, errors.New("local operation is unavailable in this conversation")
	}
	return encoded(view)
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
		Actions      []string `json:"actions"`
	}
	if !decodeInput(w, r, &input) {
		return
	}
	if input.DeviceID != "" && input.DeviceID != device {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "device id mismatch"})
		return
	}
	if input.Protocol != localagent.Protocol {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "unsupported local agent protocol"})
		return
	}
	input.Capabilities = validLocalDeclarations(input.Capabilities, 32)
	input.Actions = validLocalDeclarations(input.Actions, 32)
	task := s.localQueue.claim(device, input.Capabilities, input.Actions)
	if task == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) localAgentCancelState(w http.ResponseWriter, r *http.Request) {
	device, ok := s.localAgentAuthorized(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unknown device or bad token"})
		return
	}
	if !s.localQueue.deviceOwns(r.PathValue("operation"), device) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "operation belongs to another device"})
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
	_, operations := s.localQueue.snapshot(s.config.LocalAgent.Devices)
	for _, item := range operations {
		if item.OperationID == r.PathValue("operation") {
			writeJSON(w, http.StatusAccepted, item)
			return
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

func validLocalDeclarations(values []string, limit int) []string {
	seen := map[string]bool{}
	out := make([]string, 0, min(len(values), limit))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 64 || strings.ContainsAny(value, "\r\n\x00") || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
		if len(out) == limit {
			break
		}
	}
	sort.Strings(out)
	return out
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
	if _, _, err := s.localQueue.complete(device, receipt); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
