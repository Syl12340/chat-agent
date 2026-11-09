package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"pocket-agent/internal/probe"
	"pocket-agent/internal/tasks"
)

func (s *Server) startScheduler() {
	ctx, cancel := context.WithCancel(context.Background())
	s.schedulerCancel = cancel
	s.schedulerDone = make(chan struct{})
	s.schedulerOK.Store(true)
	go func() {
		defer close(s.schedulerDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			_, err := s.tasks.Advance(time.Now())
			old := s.schedulerOK.Swap(err == nil)
			if err != nil && old {
				log.Print("reminder scheduler could not persist; will retry")
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
func taskKind(path string) string {
	if strings.HasPrefix(path, "/api/reminders") {
		return "reminder"
	}
	return "todo"
}
func taskFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tasks.ErrNotFound):
		fail(w, 404, "待办或提醒不存在")
	case errors.Is(err, tasks.ErrConflict):
		fail(w, 409, "记录已变化或请求标识冲突，请刷新后重试")
	default:
		fail(w, 400, "操作失败，请检查标题、时间、状态或存储")
	}
}
func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	role, ok := s.memoryRole(w, r)
	if !ok {
		return
	}
	items, err := s.tasks.List(role, taskKind(r.URL.Path))
	if err != nil {
		taskFailure(w, err)
		return
	}
	state := "ok"
	if !s.schedulerOK.Load() {
		state = "retrying"
	}
	w.Header().Set("X-Pocket-Scheduler-State", state)
	writeJSON(w, 200, items)
}
func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	role, ok := s.memoryRole(w, r)
	if !ok {
		return
	}
	var input struct {
		Title string     `json:"title"`
		DueAt *time.Time `json:"due_at"`
		Key   string     `json:"idempotency_key"`
	}
	if !decodeInput(w, r, &input) {
		return
	}
	item, err := s.tasks.Add(role, taskKind(r.URL.Path), input.Title, input.Key, "manual", "", input.DueAt, time.Now())
	if err != nil {
		taskFailure(w, err)
		return
	}
	writeJSON(w, 201, item)
}
func (s *Server) taskInRoute(w http.ResponseWriter, r *http.Request, role string) bool {
	items, err := s.tasks.List(role, taskKind(r.URL.Path))
	if err != nil {
		taskFailure(w, err)
		return false
	}
	for _, item := range items {
		if item.ID == r.PathValue("id") {
			return true
		}
	}
	taskFailure(w, tasks.ErrNotFound)
	return false
}
func (s *Server) updateTask(w http.ResponseWriter, r *http.Request) {
	role, ok := s.memoryRole(w, r)
	if !ok || !s.taskInRoute(w, r, role) {
		return
	}
	var input struct {
		Status   string `json:"status"`
		Revision int    `json:"revision"`
	}
	if !decodeInput(w, r, &input) {
		return
	}
	item, err := s.tasks.Update(role, r.PathValue("id"), input.Status, input.Revision, time.Now())
	if err != nil {
		taskFailure(w, err)
		return
	}
	writeJSON(w, 200, item)
}
func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request) {
	role, ok := s.memoryRole(w, r)
	if !ok || !s.taskInRoute(w, r, role) {
		return
	}
	revision, _ := strconv.Atoi(r.URL.Query().Get("revision"))
	if err := s.tasks.Delete(role, r.PathValue("id"), revision); err != nil {
		taskFailure(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func encoded(value any) (probe.ToolResult, error) {
	data, err := json.Marshal(value)
	return probe.ToolResult{Output: string(data)}, err
}
func (s *Server) taskTools(role, sessionID string) []probe.Tool {
	var nonce [16]byte
	_, nonceErr := rand.Read(nonce[:])
	key := func(ctx context.Context) string {
		digest := sha256.Sum256([]byte(hex.EncodeToString(nonce[:]) + "\x00" + probe.ToolCallID(ctx)))
		return hex.EncodeToString(digest[:])
	}
	out := []probe.Tool{}
	for _, kind := range []string{"todo", "reminder"} {
		kind := kind
		addName, listName, finishName := "todo_add", "todo_list", "todo_complete"
		status := "done"
		addSchema := `{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}`
		if kind == "reminder" {
			addName, listName, finishName = "reminder_add", "reminder_list", "reminder_cancel"
			status = "canceled"
			addSchema = `{"type":"object","properties":{"title":{"type":"string"},"due_at":{"type":"string","description":"RFC3339 timestamp with an explicit time-zone offset; use the configured user timezone."}},"required":["title","due_at"],"additionalProperties":false}`
		}
		out = append(out, probe.Tool{Name: addName, Usage: "create only the requested item; reminder timestamps require an explicit timezone offset and deliver to the inbox.", Order: 710, Receipt: probe.ReceiptAtomic, Description: "Create a role-scoped " + kind + " only when the user asks. Reminders are one-shot inbox items, not closed-app push notifications. Identical active items are deduplicated.", Parameters: json.RawMessage(addSchema), Execute: func(ctx context.Context, data json.RawMessage) (probe.ToolResult, error) {
			if err := ctx.Err(); err != nil {
				return probe.ToolResult{}, err
			}
			if nonceErr != nil {
				return probe.ToolResult{}, errors.New("无法生成任务请求标识")
			}
			var args struct {
				Title string     `json:"title"`
				DueAt *time.Time `json:"due_at"`
			}
			if err := toolInput(data, &args); err != nil {
				return probe.ToolResult{}, err
			}
			operationID := probe.ToolOperationID(ctx)
			if operationID != "" {
				digest := sha256.Sum256(data)
				item, receipt, replayed, err := s.tasks.AddWithReceipt(role, kind, args.Title, operationID, hex.EncodeToString(digest[:]), addName, "agent", sessionID, args.DueAt, time.Now())
				if err != nil {
					return probe.ToolResult{}, errors.New("任务创建失败，请检查标题或带时区的提醒时间")
				}
				var current *tasks.Item
				if item.ID != "" {
					current = &item
				}
				result, encodeErr := encoded(struct {
					Committed bool          `json:"committed"`
					Replayed  bool          `json:"replayed"`
					Item      *tasks.Item   `json:"item,omitempty"`
					Receipt   tasks.Receipt `json:"receipt"`
				}{true, replayed, current, receipt})
				result.Receipt = &probe.BusinessReceipt{Store: "tasks", RoleID: receipt.RoleID, OperationID: receipt.OperationID, ArgumentsSHA256: receipt.ArgumentsSHA256, EntityIDs: append([]string(nil), receipt.EntityIDs...), ResultingRevisions: append([]int(nil), receipt.ResultingRevisions...), OutcomeDigest: receipt.OutcomeDigest}
				return result, encodeErr
			}
			item, err := s.tasks.Add(role, kind, args.Title, key(ctx), "agent", sessionID, args.DueAt, time.Now())
			if err != nil {
				return probe.ToolResult{}, errors.New("任务创建失败，请检查标题或带时区的提醒时间")
			}
			return encoded(item)
		}})
		out = append(out, probe.Tool{Name: listName, Usage: "read actual IDs and revisions before changing a saved item.", Order: 700, Description: "List only the current role's " + kind + " items, including IDs, states and revisions.", Parameters: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`), Execute: func(ctx context.Context, data json.RawMessage) (probe.ToolResult, error) {
			if err := ctx.Err(); err != nil {
				return probe.ToolResult{}, err
			}
			var args struct{}
			if err := toolInput(data, &args); err != nil {
				return probe.ToolResult{}, err
			}
			items, err := s.tasks.List(role, kind)
			if err != nil {
				return probe.ToolResult{}, errors.New("任务读取失败")
			}
			truncated := len(items) > 50
			if truncated {
				items = items[:50]
			}
			return encoded(struct {
				Items     []tasks.Item `json:"items"`
				Truncated bool         `json:"truncated"`
			}{items, truncated})
		}})
		out = append(out, probe.Tool{Name: finishName, Usage: "change only the requested item using its latest ID and revision.", Order: 720, Description: "Set the current role's " + kind + " to " + status + " only when explicitly requested. Read its ID and current revision from the list first.", Parameters: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"},"revision":{"type":"integer"}},"required":["id","revision"],"additionalProperties":false}`), Execute: func(ctx context.Context, data json.RawMessage) (probe.ToolResult, error) {
			if err := ctx.Err(); err != nil {
				return probe.ToolResult{}, err
			}
			var args struct {
				ID       string `json:"id"`
				Revision int    `json:"revision"`
			}
			if err := toolInput(data, &args); err != nil {
				return probe.ToolResult{}, err
			}
			items, err := s.tasks.List(role, kind)
			if err != nil {
				return probe.ToolResult{}, errors.New("任务读取失败")
			}
			for _, item := range items {
				if item.ID == args.ID {
					updated, err := s.tasks.Update(role, args.ID, status, args.Revision, time.Now())
					if err != nil {
						return probe.ToolResult{}, errors.New("记录已变化，请重新读取后操作")
					}
					return encoded(updated)
				}
			}
			return probe.ToolResult{}, errors.New("当前角色没有此任务")
		}})
	}
	return out
}

