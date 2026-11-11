package api

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	"chat-agent/internal/imagegen"
	"chat-agent/internal/pluginapi"
	"chat-agent/internal/probe"
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

// newPluginHost builds the host for one plugin. Storage exists only when the
// manifest asked for it and the host actually granted that namespace: a declared
// permission is a request, not a directory.
func newPluginHost(manifest pluginapi.Manifest, dataDir string, policy pluginapi.Policy) pluginapi.Host {
	host := pluginHost{
		id:      manifest.Metadata.ID,
		logger:  log.New(os.Stderr, "plugin["+manifest.Metadata.ID+"] ", log.LstdFlags),
		allowed: map[string]bool{},
	}
	grantedStorage := false
	for _, namespace := range manifest.Spec.Permissions.Filesystem {
		if !policy.Grant(manifest.Metadata.ID, "filesystem:"+namespace) {
			continue
		}
		host.allowed["filesystem:"+namespace] = true
		grantedStorage = true
	}
	if grantedStorage && dataDir != "" {
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
func (s *Server) pluginTools(enable pluginapi.Enablement, sessionID, roleID string) []probe.Tool {
	if s.plugins == nil {
		return nil
	}
	admission := s.plugins.Admit(enable)
	out := make([]probe.Tool, 0, len(admission.Tools))
	for _, loaded := range admission.Tools {
		if !s.hostPluginAllowed(loaded.PluginID, sessionID) {
			continue
		}
		tool := loaded.Tool
		definition := probe.Tool{
			Name:             tool.Name,
			Description:      tool.Description,
			Usage:            tool.Usage,
			Prompt:           tool.Prompt,
			Kind:             tool.Kind,
			Present:          tool.Present,
			Order:            tool.Order,
			Timeout:          tool.Timeout,
			Decision:         probe.DecisionRequirement(tool.Decision),
			Risk:             probe.RiskLevel(tool.Risk),
			ApprovalRequired: tool.ApprovalRequired,
			Receipt:          probe.ReceiptCapability(tool.Receipt),
			Parameters:       tool.Parameters,
			Execute: func(ctx context.Context, data json.RawMessage) (probe.ToolResult, error) {
				result, err := tool.Handler(ctx, data)
				if err != nil {
					return probe.ToolResult{}, err
				}
				out := probe.ToolResult{Output: result.Output}
				if len(result.Data) > 0 {
					out.Value = result.Data
				}
				if receipt := result.Receipt; receipt != nil {
					out.Receipt = &probe.BusinessReceipt{Store: receipt.Store, RoleID: roleID, OperationID: receipt.OperationID, ArgumentsSHA256: receipt.ArgumentsSHA256, EntityIDs: append([]string(nil), receipt.EntityIDs...), ResultingRevisions: append([]int(nil), receipt.ResultingRevisions...), OutcomeDigest: receipt.OutcomeDigest}
				}
				return out, nil
			},
		}
		out = append(out, definition)
	}
	return out
}

// toolCapabilities names what the current deployment may expose. The host policy
// decides: a plugin the operator switched off contributes no capability, and a
// plugin the host never granted a permission cannot open one. A zero policy (no
// configuration) exposes what the build ships, which keeps an unconfigured
// deployment working while still routing the decision through one place.
func (s *Server) toolCapabilities() pluginapi.Enablement {
	return s.pluginPolicy.Enablement(s.plugins)
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
