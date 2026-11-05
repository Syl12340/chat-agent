package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"pocket-agent/internal/companion"
	"pocket-agent/internal/interactive"
	"pocket-agent/internal/media"
	"pocket-agent/internal/probe"
	"pocket-agent/internal/turns"
)

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
		RequestID string   `json:"request_id"`
		Content   string   `json:"content"`
		Mode      string   `json:"mode"`
		Model     string   `json:"model"`
		Thinking  string   `json:"thinking"`
		ImageIDs  []string `json:"image_ids"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	var extra any
	if d.Decode(&input) != nil || d.Decode(&extra) != io.EOF || !validID(input.RequestID) || (strings.TrimSpace(input.Content) == "" && len(input.ImageIDs) == 0) || len(input.Content) > 64<<10 || !companion.ValidMode(input.Mode) || len(input.ImageIDs) > 3 {
		fail(w, 400, "消息标识、内容或交流方式无效")
		return
	}
	sess, ok := s.get(w, r)
	if !ok {
		return
	}
	uiResponse, reserved, responseErr := interactive.ParseResponse(input.Content)
	if responseErr != nil || (reserved && len(input.ImageIDs) > 0) {
		fail(w, 400, "互动答案格式无效，不能附带图片")
		return
	}
	choice, err := s.selectChat(input.Model, input.Thinking)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if len(input.ImageIDs) > 0 && !choice.Vision {
		fail(w, 400, "所选模型不支持图片，请选择可看图的模型")
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
	// Legacy clients retry their original v1 identity. Explicit selectors or
	// images opt this submission into the new versioned queue envelope.
	if input.Model != "" || input.Thinking != "" || len(images) > 0 {
		input.Model, input.Thinking = choice.Model, choice.Thinking
	}
	id := r.PathValue("id")
	if input.Mode == "" {
		input.Mode = "auto"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		fail(w, 503, "服务正在关闭")
		return
	}
	if uiResponse != nil {
		if err := s.validateUIResponse(sess, id, uiResponse); err != nil {
			fail(w, 409, err.Error())
			return
		}
	}
	if current := s.runs[id]; current != nil && !current.queued {
		fail(w, 409, "旧版聊天请求正在生成，请先停止")
		return
	}
	pending, paused, err := s.turns.HasQueued(id)
	_ = pending
	if err != nil {
		turnFailure(w, err)
		return
	}
	// Existing IDs remain queryable even when all model slots are in use.
	if s.runs[id] == nil && len(s.runs) >= 4 && !paused {
		existing, _, e := s.turns.List(id)
		if e != nil {
			turnFailure(w, e)
			return
		}
		for _, t := range existing {
			if t.ID == input.RequestID && t.Content == input.Content && t.Mode == input.Mode && t.Model == input.Model && t.Thinking == input.Thinking && sameImages(t.Images, images) {
				writeJSON(w, 200, t)
				return
			}
		}
		fail(w, 429, "同时生成的会话过多，请稍后发送")
		return
	}
	turn, duplicate, err := s.turns.EnqueueWithOptions(id, input.RequestID, input.Content, input.Mode, input.Model, input.Thinking, images)
	if err != nil {
		turnFailure(w, err)
		return
	}
	if !duplicate && !paused {
		s.startTurnWorkerLocked(id, sess)
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
	writeJSON(w, 200, map[string]any{"items": items, "paused": paused})
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
	state := &runState{cancel: cancel, done: make(chan struct{}), queued: true}
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
			timer := time.NewTimer(450 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			batch, err := s.turns.Begin(id, len(sess.Messages()), func(t turns.Turn) bool {
				_, reserved, _ := interactive.ParseResponse(t.Content)
				return !reserved
			})
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
				agent, err = s.agentForTurn(sess, query, id, func(c *probe.Config) {
					c.Model = choice.Model
					c.Thinking = choice.ProviderThinking
					c.ReasoningEffort = choice.Effort
					c.Timeout = s.chatTimeout(choice)
					if choice.Thinking != "off" {
						c.MaxOutputTokens = s.config.ThinkingMaxOutput
					}
				})
			}
			if selectionErr != nil {
				err = selectionErr
			}
			text := ""
			events := []turns.ToolEvent{}
			progressErr := error(nil)
			if err == nil {
				err = probe.RunMessages(ctx, agent, s.chatTimeout(choice), inputs, func(ev probe.Event) {
					switch ev.Type {
					case probe.EventTextDelta:
						text += ev.Text
					case probe.EventToolCallStart:
						// Keep a bounded copy of the arguments so the page can show
						// what the current call is doing instead of only its name.
						args := ev.ToolCall.Function.Arguments
						if len(args) > 240 {
							args = args[:240] + "…"
						}
						events = append(events, turns.ToolEvent{ID: ev.ToolCall.ID, Name: ev.ToolCall.Function.Name, Arguments: args})
					case probe.EventToolResult:
						for i := range events {
							if events[i].ID == ev.ToolCall.ID {
								output := ev.Result.Output
								if len(output) > 4096 {
									output = output[:4096] + "…"
								}
								events[i].Output = output
								events[i].Error = ev.Result.Error
							}
						}
					}
					if progressErr == nil {
						progressErr = s.turns.Progress(id, root, text, events)
						if progressErr != nil {
							cancel()
						}
					}
				})
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
				if len(text) > 64<<10 {
					text = string([]rune(text)[:min(len([]rune(text)), 16000)])
				}
			}
			if finishErr := s.turns.Finish(id, root, status, text, message, len(sess.Messages()), events); finishErr != nil {
				_ = s.turns.Pause(id, true)
				return
			}
			if status != "completed" {
				return
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
			s.mu.Unlock()
		}
	}()
}

// Caller holds s.mu. A chat tool may explicitly allow its own current run;
// panel mutations and all other running conversations remain blocked.
func (s *Server) roleContextCuts(role string, currentRun ...string) (map[string]int, error) {
	cuts := map[string]int{}
	files, err := os.ReadDir(filepath.Join(s.config.DataDir, probe.AgentID))
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".jsonl") || !file.Type().IsRegular() {
			continue
		}
		id := strings.TrimSuffix(file.Name(), ".jsonl")
		if !validID(id) {
			continue
		}
		sess := s.sessions[id]
		if sess == nil {
			sess, err = probe.OpenSession(s.config.DataDir, probe.AgentID, id)
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
