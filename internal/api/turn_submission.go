package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"pocket-agent/internal/companion"
	"pocket-agent/internal/interactive"
	"pocket-agent/internal/media"
	"pocket-agent/internal/probe"
	"pocket-agent/internal/turns"
)

type TurnSubmission struct {
	SessionID  string
	RequestID  string
	Content    string
	Mode       string
	Model      string
	Thinking   string
	Images     []media.Image
	DeferStart bool
}

type TurnSubmissionError struct {
	Status  int
	Message string
	Err     error
}

func (e *TurnSubmissionError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Message
}

func (e *TurnSubmissionError) Unwrap() error { return e.Err }

type TurnCompletion struct {
	SessionID   string
	RequestID   string
	RequestIDs  []string
	Text        string
	CompletedAt time.Time
}

// TurnCompletionObserver persists delivery intent only. It must not perform a
// network send from the model worker.
type TurnCompletionObserver interface {
	TurnCompleted(context.Context, TurnCompletion) error
}

func submissionError(status int, message string, err error) error {
	return &TurnSubmissionError{Status: status, Message: message, Err: err}
}

func turnSubmissionFailure(w http.ResponseWriter, err error) {
	var inputErr *TurnSubmissionError
	if errors.As(err, &inputErr) {
		fail(w, inputErr.Status, inputErr.Message)
		return
	}
	turnFailure(w, err)
}

// SubmitTurn is the single authenticated/authorized handoff into the existing
// turns queue. Callers remain responsible for authenticating their own channel.
func (s *Server) SubmitTurn(ctx context.Context, input TurnSubmission) (turns.Turn, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return turns.Turn{}, false, ctx.Err()
	default:
	}
	if input.Mode == "" {
		input.Mode = "auto"
	}
	if !validID(input.SessionID) || !validID(input.RequestID) || (strings.TrimSpace(input.Content) == "" && len(input.Images) == 0) || len(input.Content) > 64<<10 || !companion.ValidMode(input.Mode) || len(input.Images) > 3 {
		return turns.Turn{}, false, submissionError(http.StatusBadRequest, "消息标识、内容或交流方式无效", nil)
	}
	sess, err := s.session(input.SessionID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return turns.Turn{}, false, submissionError(http.StatusNotFound, "会话不存在", err)
		}
		return turns.Turn{}, false, submissionError(http.StatusServiceUnavailable, "会话不可用，请检查数据目录或锁文件", err)
	}
	response, reserved, err := interactive.ParseResponse(input.Content)
	if err != nil || (reserved && len(input.Images) > 0) {
		return turns.Turn{}, false, submissionError(http.StatusBadRequest, "互动答案格式无效，不能附带图片", err)
	}
	choice, err := s.selectChat(input.Model, input.Thinking)
	if err != nil {
		return turns.Turn{}, false, submissionError(http.StatusBadRequest, err.Error(), err)
	}
	if len(input.Images) > 0 && !choice.Vision {
		return turns.Turn{}, false, submissionError(http.StatusBadRequest, "所选模型不支持图片，请选择可看图的模型", nil)
	}
	// Legacy clients retry their original v1 identity. Explicit selectors or
	// images opt this submission into the versioned queue envelope.
	if input.Model != "" || input.Thinking != "" || len(input.Images) > 0 {
		input.Model, input.Thinking = choice.Model, choice.Thinking
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return turns.Turn{}, false, submissionError(http.StatusServiceUnavailable, "服务正在关闭", errors.New("server closing"))
	}
	if response != nil {
		if err := s.validateUIResponse(sess, input.SessionID, response); err != nil {
			return turns.Turn{}, false, submissionError(http.StatusConflict, err.Error(), err)
		}
	}
	if current := s.runs[input.SessionID]; current != nil && !current.queued {
		return turns.Turn{}, false, submissionError(http.StatusConflict, "旧版聊天请求正在生成，请先停止", nil)
	}
	_, paused, err := s.turns.HasQueued(input.SessionID)
	if err != nil {
		return turns.Turn{}, false, err
	}
	// Existing IDs remain queryable even when all model slots are in use.
	if s.runs[input.SessionID] == nil && len(s.runs) >= 4 && !paused {
		existing, _, listErr := s.turns.List(input.SessionID)
		if listErr != nil {
			return turns.Turn{}, false, listErr
		}
		for _, turn := range existing {
			if turn.ID == input.RequestID && turn.Content == input.Content && turn.Mode == input.Mode && turn.Model == input.Model && turn.Thinking == input.Thinking && sameImages(turn.Images, input.Images) {
				return turn, true, nil
			}
		}
		return turns.Turn{}, false, submissionError(http.StatusTooManyRequests, "同时生成的会话过多，请稍后发送", nil)
	}
	turn, duplicate, err := s.turns.EnqueueWithOptions(input.SessionID, input.RequestID, input.Content, input.Mode, input.Model, input.Thinking, input.Images)
	if err != nil {
		return turns.Turn{}, false, err
	}
	if !duplicate && !paused && !input.DeferStart {
		s.startTurnWorkerLocked(input.SessionID, sess)
	}
	return turn, duplicate, nil
}

// startSubmittedTurn starts a queue that was durably submitted with
// DeferStart. It is used after a channel Inbox has reached queued state.
func (s *Server) startSubmittedTurn(sessionID string, sess *probe.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("server closing")
	}
	if s.runs[sessionID] != nil {
		return nil
	}
	pending, paused, err := s.turns.HasQueued(sessionID)
	if err != nil {
		return err
	}
	if !pending || paused {
		return errors.New("submitted queue is not runnable")
	}
	if len(s.runs) >= 4 {
		_ = s.turns.Pause(sessionID, true)
		return errors.New("model worker limit reached")
	}
	s.startTurnWorkerLocked(sessionID, sess)
	return nil
}
