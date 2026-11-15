package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"chat-agent/internal/localagent"
)

func localAgentServer() *Server {
	return &Server{
		config: Config{LocalAgent: LocalAgentConfig{
			Devices:       map[string]string{"dev-box": "secret-token"},
			DefaultDevice: "dev-box",
			Wait:          2 * time.Second,
		}},
		localQueue: newLocalQueue(),
	}
}

func localRequest(t *testing.T, method, path string, body any, device, token string) *http.Request {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Chat-Agent-ID", device)
	return request
}

// A task enqueued for a device must reach that device, and its receipt must wake
// whoever is waiting. This is the loop the model and the execution endpoint meet
// in; if it breaks, both halves still look fine on their own.
func TestLocalAgentTaskRoundTrip(t *testing.T) {
	server := localAgentServer()
	task := localagent.Task{Protocol: localagent.Protocol, OperationID: "op_test_1", Kind: "read", Path: "hello.txt"}
	item := server.localQueue.enqueue(task, "dev-box", time.Minute)

	claim := httptest.NewRecorder()
	server.localAgentClaim(claim, localRequest(t, http.MethodPost, "/api/local-agent/v1/tasks/claim",
		map[string]any{"protocol": localagent.Protocol, "device_id": "dev-box"}, "dev-box", "secret-token"))
	if claim.Code != http.StatusOK {
		t.Fatalf("claim: want 200, got %d: %s", claim.Code, claim.Body.String())
	}
	var claimed localagent.Task
	if err := json.Unmarshal(claim.Body.Bytes(), &claimed); err != nil {
		t.Fatal(err)
	}
	if claimed.OperationID != "op_test_1" || claimed.Path != "hello.txt" {
		t.Fatalf("claim returned %+v", claimed)
	}

	receipt := localagent.Receipt{Protocol: localagent.Protocol, OperationID: "op_test_1", Status: "completed", Output: "contents"}
	receipt.RequestSHA256 = localTaskDigest(claimed)
	completeRequest := localRequest(t, http.MethodPost, "/api/local-agent/v1/tasks/op_test_1/complete",
		map[string]any{"device_id": "dev-box", "receipt": receipt}, "dev-box", "secret-token")
	// Calling the handler directly skips the mux, so the path variable has to be
	// set here to match what the router would extract.
	completeRequest.SetPathValue("operation", "op_test_1")
	done := httptest.NewRecorder()
	server.localAgentComplete(done, completeRequest)
	if done.Code != http.StatusNoContent {
		t.Fatalf("complete: want 204, got %d: %s", done.Code, done.Body.String())
	}

	select {
	case woke := <-item.waiting:
		if woke.Output != "contents" {
			t.Fatalf("waiting caller got %+v", woke)
		}
	case <-time.After(time.Second):
		t.Fatal("the completion never reached the waiting caller")
	}
}

func TestLocalAgentRejectsWrongToken(t *testing.T) {
	server := localAgentServer()
	recorder := httptest.NewRecorder()
	server.localAgentClaim(recorder, localRequest(t, http.MethodPost, "/api/local-agent/v1/tasks/claim",
		map[string]any{"device_id": "dev-box"}, "dev-box", "wrong-token"))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", recorder.Code)
	}
}

// An empty queue must answer 204 so the endpoint polls again rather than treating
// "nothing to do" as an error.
func TestLocalAgentClaimEmpty(t *testing.T) {
	server := localAgentServer()
	recorder := httptest.NewRecorder()
	server.localAgentClaim(recorder, localRequest(t, http.MethodPost, "/api/local-agent/v1/tasks/claim",
		map[string]any{"protocol": localagent.Protocol, "device_id": "dev-box"}, "dev-box", "secret-token"))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", recorder.Code)
	}
}

func TestLocalAgentRejectsForeignOrMismatchedReceipt(t *testing.T) {
	server := localAgentServer()
	server.config.LocalAgent.Devices["other-box"] = "other-token"
	task := localagent.Task{Protocol: localagent.Protocol, OperationID: "op_bound", Kind: "read", Path: "hello.txt"}
	server.localQueue.enqueue(task, "dev-box", time.Minute)
	if claimed := server.localQueue.claim("dev-box", []string{"read"}); claimed == nil {
		t.Fatal("task was not claimed")
	}
	receipt := localagent.Receipt{Protocol: localagent.Protocol, OperationID: task.OperationID, RequestSHA256: localTaskDigest(task), Status: "completed"}
	request := localRequest(t, http.MethodPost, "/api/local-agent/v1/tasks/op_bound/complete",
		map[string]any{"device_id": "other-box", "receipt": receipt}, "other-box", "other-token")
	request.SetPathValue("operation", task.OperationID)
	response := httptest.NewRecorder()
	server.localAgentComplete(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("foreign device receipt: want 409, got %d", response.Code)
	}
	receipt.RequestSHA256 = strings.Repeat("0", 64)
	request = localRequest(t, http.MethodPost, "/api/local-agent/v1/tasks/op_bound/complete",
		map[string]any{"device_id": "dev-box", "receipt": receipt}, "dev-box", "secret-token")
	request.SetPathValue("operation", task.OperationID)
	response = httptest.NewRecorder()
	server.localAgentComplete(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("mismatched receipt: want 409, got %d", response.Code)
	}
}

func TestLocalAgentReceiptSurvivesServerRestart(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "operations.json")
	queue := newLocalQueue(statePath)
	task := localagent.Task{Protocol: localagent.Protocol, OperationID: "op_durable", SessionID: "chat_1", RoleID: "companion", RequestID: "request_1", Kind: "share", Artifacts: []string{"result.txt"}}
	queue.enqueue(task, "dev-box", time.Minute)
	if claimed := queue.claim("dev-box", []string{"artifact-metadata"}); claimed == nil {
		t.Fatal("task was not claimed")
	}
	artifact := localagent.Artifact{Name: "result.txt", MediaType: "text/plain", Bytes: 6, SHA256: strings.Repeat("a", 64)}
	receipt := localagent.Receipt{Protocol: localagent.Protocol, OperationID: task.OperationID, RequestSHA256: localTaskDigest(task), Status: "completed", Artifacts: []localagent.Artifact{artifact}, CompletedAt: time.Now().UTC()}
	restarted := newLocalQueue(statePath)
	if restored, ok := restarted.tasks[task.OperationID]; !ok || restored.SessionID != task.SessionID || restored.RoleID != task.RoleID || restored.RequestID != task.RequestID || localTaskDigest(restored) != localTaskDigest(task) {
		t.Fatalf("full task identity did not survive restart: %+v ok=%v", restored, ok)
	}
	if _, found, err := restarted.complete("dev-box", receipt); err != nil || found {
		t.Fatalf("late completion failed: active=%v err=%v", found, err)
	}
	reopened := newLocalQueue(statePath)
	if restored, ok := reopened.artifact(task.OperationID, artifact.SHA256); !ok || restored.Name != artifact.Name {
		t.Fatalf("artifact receipt did not survive restart: %+v ok=%v", restored, ok)
	}
	_, operations := reopened.snapshot(map[string]string{"dev-box": "token"})
	if len(operations) != 1 || operations[0].Status != "completed" || operations[0].Artifacts != 1 || len(operations[0].Files) != 1 || operations[0].Files[0].DownloadURL != "/api/local-agent/artifacts/op_durable/"+artifact.SHA256 {
		t.Fatalf("operation did not survive restart: %+v", operations)
	}
	// Artifact authorization belongs to the durable operation, not only to the
	// bounded recent-receipt cache.
	reopened.receipts = map[string]localagent.Receipt{}
	reopened.order = nil
	reopened.saveLocked()
	archived := newLocalQueue(statePath)
	if restored, ok := archived.artifact(task.OperationID, artifact.SHA256); !ok || restored.Name != artifact.Name {
		t.Fatalf("artifact metadata expired with receipt cache: %+v ok=%v", restored, ok)
	}
}

// A device must not be able to collect or answer another device's work.
func TestLocalAgentRefusesForeignDevice(t *testing.T) {
	server := localAgentServer()
	server.config.LocalAgent.Devices["other-box"] = "other-token"
	server.localQueue.enqueue(localagent.Task{Protocol: localagent.Protocol, OperationID: "op_x", Kind: "read"}, "dev-box", time.Minute)

	recorder := httptest.NewRecorder()
	server.localAgentClaim(recorder, localRequest(t, http.MethodPost, "/api/local-agent/v1/tasks/claim",
		map[string]any{"device_id": "dev-box"}, "other-box", "other-token"))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("want 403 for a device_id that does not match the token, got %d", recorder.Code)
	}
}

// A task nobody claimed in time must fail the waiting caller instead of hanging.
func TestLocalAgentExpiredTaskFailsCaller(t *testing.T) {
	server := localAgentServer()
	item := server.localQueue.enqueue(localagent.Task{Protocol: localagent.Protocol, OperationID: "op_stale", Kind: "read"}, "dev-box", -time.Second)
	if task := server.localQueue.claim("dev-box", nil); task != nil {
		t.Fatalf("an expired task was handed out: %+v", task)
	}
	select {
	case receipt := <-item.waiting:
		if receipt.Status != "failed" {
			t.Fatalf("want a failed receipt, got %+v", receipt)
		}
	case <-time.After(time.Second):
		t.Fatal("expiry never notified the waiting caller")
	}
}
