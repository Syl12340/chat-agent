package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"chat-agent/internal/probe"
	"chat-agent/internal/tasks"
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
	w.Header().Set("X-Chat-Scheduler-State", state)
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

// taskTools is empty: todos and reminders are served by plugins/builtin/tasks.
