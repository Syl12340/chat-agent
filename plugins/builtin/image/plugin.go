// Package image exposes the local renderer's health to the model.
//
// It lives here rather than in the API package because it needs nothing but the
// generator interface: no session, no store, no host state. The API package kept it
// only because every tool started there.
package image

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"chat-agent/internal/imagegen"
	"chat-agent/internal/pluginapi"
)

const PluginID = "builtin.image"

// New returns nil when no renderer is configured, so the loader drops the plugin
// instead of advertising a tool that can only answer "not configured".
func New(generator imagegen.Generator) pluginapi.Plugin {
	if generator == nil {
		return nil
	}
	return &Plugin{
		generator: generator,
		manifest: pluginapi.Manifest{
			APIVersion: pluginapi.APIVersion,
			Kind:       pluginapi.Kind,
			Metadata: pluginapi.Metadata{
				ID:          PluginID,
				Name:        "图像生成",
				Version:     "0.1.0",
				Description: "报告本机渲染器是否可用、哪些配置能画、GPU 与队列情况",
			},
			Spec: pluginapi.Spec{
				Runtime:      pluginapi.RuntimeBuiltin,
				Capabilities: []string{"image.health"},
				Tools:        []string{"image_health"},
			},
		},
	}
}

// Plugin answers what the renderer can actually do right now.
type Plugin struct {
	generator imagegen.Generator
	manifest  pluginapi.Manifest
}

func (p *Plugin) Manifest() pluginapi.Manifest                { return p.manifest }
func (p *Plugin) Start(context.Context, pluginapi.Host) error { return nil }
func (p *Plugin) Stop(context.Context) error                  { return nil }

func (p *Plugin) Register(registry *pluginapi.Registry) error {
	return registry.Tool(p.healthTool())
}

func (p *Plugin) healthTool() pluginapi.Tool {
	return pluginapi.Tool{
		Name:    "image_health",
		Kind:    "status",
		Order:   655,
		Timeout: 25 * time.Second,
		Usage: "check before drawing if the last image attempt failed, returned 'busy', or the user asks whether pictures are possible right now; it is cheap. " +
			"Do not call it before every drawing — one healthy answer stays valid for the rest of the turn.",
		Description: "Report the state of the local image renderer: whether it is ready, which profiles can actually render now, " +
			"and the GPU and queue situation. Ready=false means a drawing attempt will probably fail; the reason field says why in plain words. " +
			"A profile listed here is safe to pass to image_generate. While ready is true you can still expect a single render to take 10-60 seconds.",
		Parameters: json.RawMessage(`{"type":"object","properties":{},"required":[],"additionalProperties":false}`),
		Handler: func(ctx context.Context, _ json.RawMessage) (pluginapi.Result, error) {
			status := p.generator.Status(ctx)
			profiles := append([]string(nil), status.Profiles...)
			sort.Strings(profiles)
			payload := struct {
				Ready    bool           `json:"ready"`
				Reason   string         `json:"reason,omitempty"`
				Profiles []string       `json:"profiles"`
				Default  string         `json:"default,omitempty"`
				Details  map[string]any `json:"details,omitempty"`
			}{status.Ready, status.Reason, profiles, status.Default, status.Details}
			body, err := json.Marshal(payload)
			if err != nil {
				return pluginapi.Result{}, err
			}
			// One readable line alongside the JSON: a bare object is easy to skim
			// past, and the whole point is that the model acts on this.
			return pluginapi.Result{Output: fmt.Sprintf("%s\n%s", summarise(status), body)}, nil
		},
	}
}

// summarise turns the probe into one sentence the model can act on.
func summarise(status imagegen.Status) string {
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
		return b.String()
	}
	b.WriteString("图像生成当前不可用")
	if status.Reason != "" {
		b.WriteString("：")
		b.WriteString(status.Reason)
	}
	b.WriteString("。可以稍后再试，或直接告诉用户现在画不了。")
	return b.String()
}
