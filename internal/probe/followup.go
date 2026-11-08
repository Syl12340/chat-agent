package probe

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// FollowupFinalizer validates a complete proactive response while the session
// writer is still held. Returning persist=false records the attempt without
// manufacturing a visible chat message (for example, a deliberate silence).
type FollowupFinalizer func(Message, int) (message Message, persist bool, err error)

// RunFollowup performs one host-triggered, tool-free model step against an
// existing conversation and optionally commits one assistant message. The
// trigger itself belongs in Config.Reference and remains an ephemeral runtime
// snapshot; it is never persisted as a user message or treated as consent.
func RunFollowup(ctx context.Context, a *Agent, timeout time.Duration, finalize FollowupFinalizer, emit func(Event)) (message Message, persisted bool, runErr error) {
	if a == nil || timeout <= 0 || finalize == nil || a.config.MaxTurns != 1 {
		return Message{}, false, errors.New("invalid proactive followup")
	}
	if !a.session.mu.TryLock() {
		return Message{}, false, errors.New("session is busy")
	}
	defer a.session.mu.Unlock()
	if a.session.closed {
		return Message{}, false, errors.New("session is closed")
	}
	if timeout > a.config.Timeout {
		timeout = a.config.Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	send := func(event Event) {
		if emit != nil {
			emit(event)
		}
	}
	defer func() {
		if ctx.Err() != nil {
			runErr = errors.Join(context.Cause(ctx), runErr)
		}
		if runErr != nil {
			send(Event{Type: EventError, Error: runErr})
		} else {
			send(Event{Type: EventDone})
		}
	}()
	historyFrom := len(a.session.Messages())
	if a.config.Recorder != nil {
		if err := a.config.Recorder.AttemptStarted(ctx, AttemptRecord{SessionID: a.config.SessionID, RoleID: a.config.RoleID, RequestID: a.config.RequestID, HistoryFrom: historyFrom, Inputs: []Message{}, MaxTurns: 1, MaxTools: 0}); err != nil {
			return Message{}, false, recordingError("attempt start", err)
		}
		defer func() {
			status, reason := "completed", "final_response"
			if !persisted && runErr == nil {
				reason = "no_visible_response"
			}
			if runErr != nil {
				status, reason = "failed", "runtime_error"
			}
			if ctx.Err() != nil || errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
				status, reason = "canceled", "context_canceled"
			}
			finish := AttemptFinish{Status: status, StopReason: reason}
			if runErr != nil {
				finish.Error = runErr.Error()
			}
			if err := a.config.Recorder.AttemptFinished(context.WithoutCancel(ctx), finish); err != nil {
				runErr = errors.Join(runErr, recordingError("attempt finish", err))
			}
		}()
	}
	if err := ctx.Err(); err != nil {
		return Message{}, false, err
	}
	a.turnStart = historyFrom
	assembly, err := a.assemble(ctx, a.stepContext(0, 0, false))
	if err != nil {
		return Message{}, false, err
	}
	if len(assembly.Tools.ordered) != 0 {
		return Message{}, false, errors.New("proactive followup exposed tools")
	}
	if a.config.ContextBytes > 0 {
		data, _ := json.Marshal(modelVisibleMessages(assembly.Messages))
		if len(data) > a.config.ContextBytes {
			return Message{}, false, errors.New("proactive followup context exceeds limit")
		}
	}
	response, err := a.stream(ctx, 0, assembly, send)
	if err != nil {
		return Message{}, false, err
	}
	if len(response.ToolCalls) != 0 {
		return Message{}, false, errors.New("proactive followup requested unavailable tools")
	}
	message, persisted, err = finalize(response, historyFrom)
	if err != nil {
		return Message{}, false, err
	}
	if !persisted {
		return message, false, nil
	}
	if message.Role != "assistant" || message.Source != "" || strings.TrimSpace(message.Content) == "" || len(message.ToolCalls) != 0 || message.ToolCallID != "" || len(message.Images) != 0 || !persistentMessageValid(message) {
		return Message{}, false, errors.New("invalid proactive followup response")
	}
	if err = a.session.append(message); err != nil {
		return Message{}, false, err
	}
	return message, true, nil
}
