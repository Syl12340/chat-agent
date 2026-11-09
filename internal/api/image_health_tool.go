package api

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"chat-agent/internal/imagegen"
	"chat-agent/internal/probe"
)

// imageHealthTool lets the model ask what the renderer can do instead of the host
// deciding for it.
//
// This is the point of a health probe: when the renderer is busy, missing a
// checkpoint or behind a dropped tunnel, that is a fact the model can reason
// about — it might wait, pick another profile, or tell the user. Hiding the
// capability instead would leave the model with no explanation and the user with
// no answer.
func (s *Server) imageHealthTool() probe.Tool {
	return probe.Tool{
		Name:   "image_health",
		Order:  655,
		Usage:  "check before drawing if the last image attempt failed, returned 'busy', or the user asks whether pictures are possible right now; it is cheap. Do not call it before every drawing — one healthy answer stays valid for the rest of the turn.",
		Timeout: 25 * time.Second,
		Description: "Report the state of the local image renderer: whether it is ready, which profiles can actually render now, " +
			"and the GPU and queue situation. Ready=false means a drawing attempt will probably fail; the reason field says why in plain words. " +
			"A profile listed here is safe to pass to image_generate. While ready is true you can still expect a single render to take 10-60 seconds.",
		Parameters: json.RawMessage(`{"type":"object","properties":{},"required":[],"additionalProperties":false}`),
		Execute: func(ctx context.Context, data json.RawMessage) (probe.ToolResult, error) {
			if s.imagegen == nil {
				return encoded(struct {
					Ready  bool   `json:"ready"`
					Reason string `json:"reason"`
				}{false, "未配置图像生成服务"})
			}
			status := s.imagegen.Status(ctx)
			profiles := append([]string(nil), status.Profiles...)
			sort.Strings(profiles)
			payload := struct {
				Ready    bool           `json:"ready"`
				Reason   string         `json:"reason,omitempty"`
				Profiles []string       `json:"profiles"`
				Default  string         `json:"default,omitempty"`
				Details  map[string]any `json:"details,omitempty"`
			}{status.Ready, status.Reason, profiles, status.Default, status.Details}
			result, err := encoded(payload)
			if err != nil {
				return probe.ToolResult{}, err
			}
			// Give the model one readable line alongside the JSON: a bare object is
			// easy to skim past, and the whole point is that it acts on this.
			result.Output = fmt.Sprintf("%s\n%s", summariseImageStatus(status), result.Output)
			return result, nil
		},
	}
}

// summariseImageStatus turns the probe into one sentence the model can act on.
func summariseImageStatus(status imagegen.Status) string {
	var b strings.Builder
	if status.Ready {
		b.WriteString("图像生成可用")
		if len(status.Profiles) > 0 {
			b.WriteString("，可用配置：")
			b.WriteString(strings.Join(status.Profiles, "、"))
		}
		if status.Default != "" {
			b.WriteString("；默认 ")
			b.WriteString(status.Default)
		}
		b.WriteString("。")
	} else {
		b.WriteString("图像生成当前不可用")
		if status.Reason != "" {
			b.WriteString("：")
			b.WriteString(status.Reason)
		}
		b.WriteString("。可以稍后再试，或直接告诉用户现在画不了。")
	}
	return b.String()
}
