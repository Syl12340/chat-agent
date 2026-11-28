package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"chat-agent/internal/interactive"
	"chat-agent/internal/media"
	"chat-agent/internal/probe"
	"chat-agent/internal/runlog"
	"chat-agent/internal/turns"
)

const (
	turnMergeDebounce = 650 * time.Millisecond
	turnMergeMaxWait  = 1800 * time.Millisecond
)

func waitTurnMergeWindow(ctx context.Context, arrival <-chan struct{}, debounce, maxWait time.Duration) bool {
	deadline := time.Now().Add(maxWait)
	timer := time.NewTimer(debounce)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		case <-arrival:
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return true
			}
			next := min(debounce, remaining)
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(next)
		}
	}
}

func turnFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, turns.ErrConflict):
		fail(w, 409, "消息标识已使用或状态已变化，请刷新查看")
	case errors.Is(err, turns.ErrLimit):
		fail(w, 429, "待发送消息过多或会话已达上限，请稍后发送或新建会话")
	case errors.Is(err, turns.ErrNotFound):
		fail(w, 404, "消息不存在")
	default:
		fail(w, 503, "消息队列暂时不可用，请保留消息并稍后重试")
	}
}
func (s *Server) enqueueTurn(w http.ResponseWriter, r *http.Request) {
	s.mediaMu.Lock()
	defer s.mediaMu.Unlock()
	var input struct {
		RequestID  string   `json:"request_id"`
		Content    string   `json:"content"`
		Mode       string   `json:"mode"`
		Model      string   `json:"model"`
		Thinking   string   `json:"thinking"`
		ImageIDs   []string `json:"image_ids"`
		Supplement bool     `json:"supplement"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	var extra any
	if d.Decode(&input) != nil || d.Decode(&extra) != io.EOF || len(input.ImageIDs) > 3 {
		fail(w, 400, "消息标识、内容或交流方式无效")
		return
	}
	sess, ok := s.get(w, r)
	if !ok {
		return
	}
	images := []media.Image{}
	seen := map[string]bool{}
	for _, imageID := range input.ImageIDs {
		if seen[imageID] {
			fail(w, 400, "同一张图片不能在一条消息中重复提交")
			return
		}
		seen[imageID] = true
		image, _, err := s.images.Get(sess.RoleID(), r.PathValue("id"), imageID)
		if err != nil {
			imageFailure(w, err)
			return
		}
		images = append(images, image)
	}
	id := r.PathValue("id")
	turn, duplicate, err := s.SubmitTurn(r.Context(), TurnSubmission{SessionID: id, RequestID: input.RequestID, Content: input.Content, Mode: input.Mode, Model: input.Model, Thinking: input.Thinking, Images: images, Supplement: input.Supplement})
	if err != nil {
		turnSubmissionFailure(w, err)
		return
	}
	code := http.StatusAccepted
	if duplicate {
		code = 200
	}
	writeJSON(w, code, turn)
}
func (s *Server) listTurns(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.get(w, r); !ok {
		return
	}
	items, paused, err := s.turns.List(r.PathValue("id"))
	if err != nil {
		turnFailure(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "paused": paused, "protocol_version": remoteProtocolVersion, "generation": s.transportGeneration, "resync": s.transportResync(r)})
}
func (s *Server) resumeTurns(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.get(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		fail(w, 503, "服务正在关闭")
		return
	}
	if s.runs[id] != nil {
		fail(w, 409, "会话正在运行")
		return
	}
	if len(s.runs) >= 4 {
		fail(w, 429, "同时生成的会话过多")
		return
	}
	if err := s.turns.Pause(id, false); err != nil {
		turnFailure(w, err)
		return
	}
	s.startTurnWorkerLocked(id, sess)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
// taskContracts answers with the four-level view of every turn in a session, so a
// page can render task state from one source instead of deriving it per message.
func (s *Server) taskContracts(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.get(w, r); !ok {
		return
	}
	id := r.PathValue("id")
	items, _, err := s.turns.List(id)
	if err != nil {
		turnFailure(w, err)
		return
	}
	out := make([]turns.Task, 0, len(items))
	for _, turn := range items {
		out = append(out, turns.ContractOf(id, turn))
	}
	writeJSON(w, 200, out)
}

// taskContract answers with the four-level view of one turn, so a caller does not
// have to reconstruct task state from turn fields and tool events itself.
func (s *Server) taskContract(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.get(w, r); !ok {
		return
	}
	id := r.PathValue("id")
	items, _, err := s.turns.List(id)
	if err != nil {
		turnFailure(w, err)
		return
	}
	for _, turn := range items {
		if turn.ID == r.PathValue("turn") {
			writeJSON(w, 200, turns.ContractOf(id, turn))
			return
		}
	}
	fail(w, http.StatusNotFound, "没有这个任务")
}

func (s *Server) withdrawTurn(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.get(w, r)
	if !ok {
		return
	}
	// ?purge=1 physically removes the turn instead of marking it withdrawn.
	if r.URL.Query().Get("purge") == "1" {
		s.purgeTurn(w, r, sess)
		return
	}
	if err := s.turns.Withdraw(r.PathValue("id"), r.PathValue("request")); err != nil {
		turnFailure(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// Called with s.mu held. A worker slot includes the short aggregation window.
func (s *Server) startTurnWorkerLocked(id string, sess *probe.Session) {
	if s.runs[id] != nil {
		return
	}
	ctx, cancel := context.WithCancel(s.workCtx)
	state := &runState{cancel: cancel, done: make(chan struct{}), queued: true, arrival: make(chan struct{}, 1)}
	s.runs[id] = state
	s.workWG.Add(1)
	go func() {
		released := false
		defer s.workWG.Done()
		defer func() {
			cancel()
			if !released {
				s.mu.Lock()
				if s.runs[id] == state {
					delete(s.runs, id)
					close(state.done)
				}
				s.mu.Unlock()
			}
		}()
		for {
			if !waitTurnMergeWindow(ctx, state.arrival, turnMergeDebounce, turnMergeMaxWait) {
				return
			}
			// Close the aggregation window and select its durable batch while holding
			// the admission mutex. A sender arriving after this point cannot race into
			// the batch after being told it was queued for the next turn.
			s.mu.Lock()
			if s.runs[id] == state {
				state.queued = false
			}
			batch, err := s.turns.Begin(id, len(sess.Messages()), func(t turns.Turn) bool {
				_, reserved, _ := interactive.ParseResponse(t.Content)
				return !reserved
			})
			s.mu.Unlock()
			if err != nil {
				_ = s.turns.Pause(id, true)
				return
			}
			if len(batch) == 0 {
				s.mu.Lock()
				more, paused, e := s.turns.HasQueued(id)
				if e != nil || !more || paused {
					if s.runs[id] == state {
						delete(s.runs, id)
						close(state.done)
					}
					released = true
					s.mu.Unlock()
					return
				}
				state.queued = true
				s.mu.Unlock()
				continue
			}
			root := batch[0].ID
			inputs := []probe.Message{}
			query := []string{}
			for _, t := range batch {
				inputs = append(inputs, probe.Message{Role: "user", Content: t.Content, Images: t.Images})
				query = append(query, t.Content)
			}
			choice, selectionErr := s.selectChat(batch[0].Model, batch[0].Thinking)
			var agent *probe.Agent
			var recorder *runlog.Recorder
			// Recheck a queued answer before building an agent or appending a
			// user message: context may have changed while the queue was paused.
			for _, t := range batch {
				response, _, parseErr := interactive.ParseResponse(t.Content)
				if parseErr != nil {
					err = parseErr
					break
				}
				if response != nil {
					if err = s.validateUIResponse(sess, id, response); err != nil {
						break
					}
				}
			}
			if err == nil {
				recorder, err = runlog.Open(s.runtime.Root(), runlog.Scope{SessionID: id, RoleID: sess.RoleID(), RequestID: root})
			}
			if err == nil {
				agent, err = s.agentForTurn(sess, query, id, func(c *probe.Config) {
					s.applyChatSelection(c, choice)
					c.RequestID = root
					c.Recorder = recorder
					c.InteractiveApproval = batch[0].Origin.Kind == "" && !s.publicChannelSession(id)
				})
				if err != nil {
					// Assembly failures (role, memory, tools) look identical from
					// the queue, so the operator gets the reason here.
					log.Printf("turn agent unavailable: %v", err)
				}
				if agent != nil {
					s.mu.Lock()
					if s.runs[id] == state {
						state.agent = agent
					}
					s.mu.Unlock()
				}
			}
			if selectionErr != nil {
				err = selectionErr
			}
			text := ""
			reasoning := ""
			events := []turns.ToolEvent{}
			var toolStarted time.Time
			progressErr := error(nil)
			if err == nil {
				err = probe.RunMessages(ctx, agent, s.chatTimeout(choice), inputs, func(ev probe.Event) {
					switch ev.Type {
					case probe.EventThinking:
						if reasoning != "" {
							reasoning += "\n\n"
						}
					case probe.EventReasoningDelta:
						if len(reasoning)+len(ev.Text) > 1<<20 {
							progressErr = turns.ErrLimit
							cancel()
							return
						}
						reasoning += ev.Text
					case probe.EventTextDelta:
						if len(text)+len(ev.Text) > 4<<20 {
							progressErr = turns.ErrLimit
							cancel()
							return
						}
						text += ev.Text
					case probe.EventToolCallStart:
						toolStarted = time.Now()
						events = append(events, startToolView(ev))
					case probe.EventToolResult:
						if last := len(events) - 1; last >= 0 && events[last].ID == ev.ToolCall.ID {
							finishToolView(&events[last], ev.Result, time.Since(toolStarted))
						}
					}
					if progressErr == nil {
						progressErr = s.turns.ProgressWithReasoning(id, root, text, reasoning, events)
						if progressErr != nil {
							cancel()
						}
					}
				})
			}
			if recorder != nil {
				if closeErr := recorder.Close(); closeErr != nil {
					err = errors.Join(err, closeErr)
				}
			}
			status, message := "completed", ""
			if err != nil || progressErr != nil {
				status = "failed"
				failure := err
				if progressErr != nil {
					failure = progressErr
				}
				code, detail := generationFailure(failure)
				if progressErr != nil {
					code, detail = "progress_save", "生成进度保存失败，请查看已保存记录；没有自动重试。"
				}
				message = detail
				if progressErr == nil && (errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled)) {
					status = "canceled"
					message = "已停止；排队消息保留，点击继续才会再次调用模型。"
				}
				if s.workCtx.Err() != nil {
					status = "interrupted"
					message = "服务曾中断；没有自动重试模型或工具。"
				}
				if status == "failed" {
					log.Printf("chat turn failed: category=%s", code)
				}
			}
			if status == "completed" {
				// A reply is not the same thing as the work having happened. When
				// attempts to change something outside the transcript failed, the
				// reply must not read as a finished job - the model said it did the
				// thing, but the evidence in this turn says otherwise.
				if failures := failedSideEffects(events); failures > 0 {
					message = fmt.Sprintf("本轮有 %d 个操作失败，结果可能不完整。", failures)
				}
			}
			if finishErr := s.turns.FinishWithReasoning(id, root, status, text, reasoning, message, len(sess.Messages()), events); finishErr != nil {
				_ = s.turns.Pause(id, true)
				return
			}
			if status != "completed" {
				return
			}
			if observer := s.turnObserver; observer != nil {
				requestIDs := make([]string, 0, len(batch))
				for _, turn := range batch {
					requestIDs = append(requestIDs, turn.ID)
				}
				if observeErr := observer.TurnCompleted(ctx, TurnCompletion{SessionID: id, RequestID: root, RequestIDs: requestIDs, Text: text, Origin: batch[0].Origin, CompletedAt: time.Now().UTC()}); observeErr != nil {
					log.Printf("turn completion observer failed")
					_ = s.turns.Pause(id, true)
					return
				}
			}
			// Serialize exit with enqueues: a newly accepted message cannot lose its worker.
			s.mu.Lock()
			more, paused, e := s.turns.HasQueued(id)
			if e != nil || !more || paused {
				if s.runs[id] == state {
					delete(s.runs, id)
					close(state.done)
				}
				released = true
				s.mu.Unlock()
				return
			}
			// Open a fresh short merge window for messages that accumulated while
			// the previous provider call was running. They remain a separate turn.
			state.queued = true
			s.mu.Unlock()
		}
	}()
}

// Caller holds s.mu. A chat tool may explicitly allow its own current run;
// panel mutations and all other running conversations remain blocked.
// sideEffecting names the tools that change something outside the transcript.
// A reply claiming the work is done carries no weight when these failed: the only
// evidence that something happened is that the tool reported success.
var sideEffecting = map[string]bool{
	"workspace_write": true,
	"workspace_shell": true,
	"local_workspace": true,
	"image_generate":  true,
	"image_cancel":    true,
	"server_action":   true,
	"reminder_add":    true,
	"reminder_cancel": true,
	"todo_add":        true,
	"todo_complete":   true,
	"memory_write":    true,
	"knowledge_manage": true,
}

// failedSideEffects counts side-effecting tool calls that reported an error.
// Read-only tools are ignored: a failed search leaves nothing to be wrong about.
func failedSideEffects(events []turns.ToolEvent) int {
	failed := 0
	for _, event := range events {
		if sideEffecting[event.Name] && strings.TrimSpace(event.Error) != "" {
			failed++
		}
	}
	return failed
}

func (s *Server) roleContextCuts(role string, currentRun ...string) (map[string]int, error) {
	cuts := map[string]int{}
	entries, err := s.runtime.Entries()
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.RoleID != role {
			continue
		}
		id := entry.ID
		sess := s.sessions[id]
		if sess == nil {
			historyPath, pathErr := s.runtime.HistoryPath(id)
			if pathErr != nil {
				return nil, pathErr
			}
			sess, err = probe.OpenRuntimeSession(historyPath)
			if err != nil {
				return nil, err
			}
			s.sessions[id] = sess
		}
		if sess.RoleID() == role {
			if s.runs[id] != nil && (len(currentRun) != 1 || currentRun[0] != id) {
				return nil, errors.New("role conversation is running")
			}
			cuts[id] = len(sess.Messages())
		}
	}
	return cuts, nil
}
