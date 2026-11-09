package api

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	"pocket-agent/internal/imagegen"
	"pocket-agent/internal/pluginapi"
	"pocket-agent/internal/probe"
)

// pluginHost is the narrow surface a built-in plugin receives. It hands out a
// prefixed logger and a per-plugin directory, and nothing else: no session
// access, no model key, no way to reach another plugin's data.
type pluginHost struct {
	id      string
	logger  *log.Logger
	dir     string
	allowed map[string]bool
}

func (h pluginHost) Logger() *log.Logger { return h.logger }

func (h pluginHost) DataDir() string { return h.dir }

func (h pluginHost) Allowed(permission string) bool {
	if h.allowed == nil {
		return false
	}
	return h.allowed[permission]
}

// newPluginHost builds the host for one plugin, creating its storage directory
// only when the manifest asked for one.
func newPluginHost(manifest pluginapi.Manifest, dataDir string) pluginapi.Host {
	host := pluginHost{
		id:      manifest.Metadata.ID,
		logger:  log.New(os.Stderr, "plugin["+manifest.Metadata.ID+"] ", log.LstdFlags),
		allowed: map[string]bool{},
	}
	for _, namespace := range manifest.Spec.Permissions.Filesystem {
		host.allowed["filesystem:"+namespace] = true
	}
	if len(manifest.Spec.Permissions.Filesystem) > 0 && dataDir != "" {
		dir := filepath.Join(dataDir, "plugins", manifest.Metadata.ID)
		if err := os.MkdirAll(dir, 0o700); err == nil {
			host.dir = dir
		}
	}
	return host
}

// pluginTools turns admitted plugin tools into the tool definitions the harness
// executes. This is the single seam where a plugin capability becomes a model
// tool; everything after it — schema validation, guards, run recording, receipts
// — stays in the existing harness path.
//
// The wrap keeps the error surface identical to built-in tools so callers cannot
// tell a plugin tool from a core one.
func (s *Server) pluginTools(enable pluginapi.Enablement) []probe.Tool {
	if s.plugins == nil {
		return nil
	}
	admission := s.plugins.Admit(enable)
	out := make([]probe.Tool, 0, len(admission.Tools))
	for _, loaded := range admission.Tools {
		tool := loaded.Tool
		pluginID := loaded.PluginID
		out = append(out, probe.Tool{
			Name:        tool.Name,
			Description: tool.Description,
			Usage:       tool.Usage,
			Order:       tool.Order,
			Timeout:     tool.Timeout,
			Parameters:  tool.Parameters,
			Execute: func(ctx context.Context, data json.RawMessage) (probe.ToolResult, error) {
				result, err := tool.Handler(ctx, data)
				if err != nil {
					return probe.ToolResult{}, err
				}
				out := probe.ToolResult{Output: result.Output}
				if len(result.Data) > 0 {
					out.Value = result.Data
				}
				_ = pluginID
				return out, nil
			},
		})
	}
	return out
}

// toolCapabilities names what the current deployment may expose. Until roles carry
// per-capability grants, a built-in plugin is available to every turn that can use
// tools at all; the indirection exists so that decision has one home.
func (s *Server) toolCapabilities() pluginapi.Enablement {
	return pluginapi.AllowAll()
}

// imageGenerator picks the image backend: a tunnelled remote bridge wins over the
// local CLI, because a configured tunnel means the operator deliberately moved the
// GPU elsewhere.
//
// Both constructors return nil when unconfigured. Returning a nil *Client through
// the Generator interface would make it non-nil and defeat every later check, so
// each branch tests the concrete value before assigning.
func imageGenerator(config Config) imagegen.Generator {
	if remote := imagegen.NewRemote(config.ImageRemote); remote != nil {
		return remote
	}
	if client := imagegen.New(config.Images); client != nil && client.Enabled() {
		return client
	}
	return nil
}

// imageAvailable reports whether image generation can be used right now. A remote
// backend is only available while its health probe succeeds, so the page can stop
// offering the capability the moment the tunnel drops.
func (s *Server) imageAvailable() bool {
	if s.imagegen == nil {
		return false
	}
	if remote, ok := s.imagegen.(*imagegen.Remote); ok {
		return remote.Healthy()
	}
	return true
}

// imageUnavailableReason explains a missing capability in words the page can show.
func (s *Server) imageUnavailableReason() string {
	if s.imagegen == nil {
		return "未配置图像生成服务"
	}
	if remote, ok := s.imagegen.(*imagegen.Remote); ok {
		if reason := remote.Reason(); reason != "" {
			return reason
		}
	}
	return ""
}
