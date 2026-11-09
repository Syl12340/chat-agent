package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"chat-agent/internal/companion"
	"chat-agent/internal/probe"
)

func companionFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, companion.ErrConflict):
		fail(w, 409, "偏好已在别处修改，请重新打开后保存")
	case errors.Is(err, companion.ErrInvalid):
		fail(w, 400, "请检查称呼、偏好选项和版本；称呼最多 40 字且不能包含控制字符")
	default:
		fail(w, 503, "陪伴偏好暂时不可用，请稍后重试")
	}
}
func (s *Server) getCompanion(w http.ResponseWriter, r *http.Request) {
	if _, err := s.roles.Load(r.PathValue("id")); err != nil {
		fail(w, 404, "角色不存在")
		return
	}
	snap, err := s.companion.Get(r.PathValue("id"))
	if err != nil {
		companionFailure(w, err)
		return
	}
	writeJSON(w, 200, snap)
}
func (s *Server) updateCompanion(w http.ResponseWriter, r *http.Request) {
	if _, err := s.roles.Load(r.PathValue("id")); err != nil {
		fail(w, 404, "角色不存在")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var patch companion.Patch
	if !decodeInput(w, r, &patch) {
		return
	}
	snap, err := s.companion.Update(r.PathValue("id"), patch)
	if err != nil {
		companionFailure(w, err)
		return
	}
	writeJSON(w, 200, snap)
}
func (s *Server) companionContext(role string, inputs []string, mode string) (string, string, []probe.Tool, error) {
	snap, err := s.companion.Get(role)
	if err != nil {
		return "", "", nil, err
	}
	policy, reference := companion.Context(snap, mode, role == "companion")
	tool := probe.Tool{Name: "companion_preferences_update", Usage: "save an ongoing communication preference, not a one-off request.", Order: 500, Description: "Save only the ongoing communication preferences explicitly requested in the latest user message. Copy a short exact quote from a current-turn user message into evidence. Interpret natural language and context, without command syntax. Include current preference revision. Omit every unrequested field; never save temporary style, feelings, personal facts or content quoted from other sources. Successful output contains the new saved preferences; only then acknowledge saving.", Parameters: json.RawMessage(`{"type":"object","properties":{"revision":{"type":"integer","minimum":0},"evidence":{"type":"string","maxLength":1024},"preferred_name":{"type":"string","maxLength":40},"reply_length":{"type":"string","enum":["default","short","detailed"]},"tone":{"type":"string","enum":["default","natural","warm","playful"]},"advice":{"type":"string","enum":["default","ask_first","on_request"]},"questions":{"type":"string","enum":["default","light","normal"]}},"required":["revision","evidence"],"additionalProperties":false}`), Execute: func(ctx context.Context, data json.RawMessage) (probe.ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return probe.ToolResult{}, err
		}
		var args struct {
			companion.Patch
			Evidence string `json:"evidence"`
		}
		if err := toolInput(data, &args); err != nil {
			return probe.ToolResult{}, err
		}
		if !turnHasEvidence(inputs, args.Evidence) {
			return probe.ToolResult{}, errors.New("偏好修改缺少当前用户消息中的依据")
		}
		current, err := s.companion.Get(role)
		if err != nil {
			return probe.ToolResult{}, errors.New("陪伴偏好暂时不可用")
		}
		if args.Revision != current.Revision {
			return probe.ToolResult{}, errors.New("偏好版本不匹配，请读取当前设置后重试")
		}
		saved, err := s.companion.Update(role, args.Patch)
		if errors.Is(err, companion.ErrConflict) {
			return probe.ToolResult{}, errors.New("偏好已在别处修改，请重新打开设置")
		}
		if err != nil {
			return probe.ToolResult{}, errors.New("偏好保存失败，请检查字段或使用陪伴偏好设置")
		}
		out, _ := json.Marshal(saved)
		return probe.ToolResult{Output: string(out)}, nil
	}}
	return policy, reference, []probe.Tool{tool}, nil
}
