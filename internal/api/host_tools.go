package api

import (
	"errors"
	"slices"

	"chat-agent/internal/pluginapi"
	serverdiag "chat-agent/plugins/builtin/server"
	"chat-agent/plugins/builtin/workspace"
)

// HostToolsConfig scopes machine access to explicitly approved conversations.
type HostToolsConfig struct {
	Sessions         []string
	WorkspaceRoot    string
	WorkspaceActions string
	// ServiceUnits is the allow-list of systemd units the diagnostics plugin may
	// inspect. Empty leaves the per-unit tool unregistered.
	ServiceUnits []string
	// ServiceActions is an operator-owned JSON map of action id to fixed argv.
	// It is inert unless the maintenance permission is separately granted.
	ServiceActions string
}

func (c Config) hostPlugins() ([]pluginapi.Plugin, error) {
	for _, id := range c.HostTools.Sessions {
		if !validID(id) {
			return nil, errors.New("invalid host-tool session id")
		}
	}
	// Build when the deployment configures something to look at, not when a
	// conversation is already allowed to see it: a runtime grant needs the
	// plugins to exist before anybody is granted them. Who may see them is
	// decided per session in hostPluginAllowed.
	if c.HostTools.WorkspaceRoot == "" && len(c.HostTools.ServiceUnits) == 0 {
		return nil, nil
	}
	plugins := []pluginapi.Plugin{}
	if c.Plugins.PluginEnabled(workspace.ID) && c.Plugins.Grant(workspace.ID, workspace.Permission) && c.HostTools.WorkspaceRoot != "" {
		p, err := workspace.New(c.HostTools.WorkspaceRoot, c.DataDir, c.HostTools.WorkspaceActions)
		if err != nil {
			return nil, err
		}
		plugins = append(plugins, p)
	}
	if c.Plugins.Grant(serverdiag.ID, serverdiag.Permission) {
		actions := ""
		if c.Plugins.Grant(serverdiag.ID, serverdiag.MaintenancePermission) {
			actions = c.HostTools.ServiceActions
		}
		p, err := serverdiag.NewWithActions(c.HostTools.ServiceUnits, actions)
		if err != nil {
			return nil, err
		}
		if p != nil {
			plugins = append(plugins, p)
		}
	}
	return plugins, nil
}

func (s *Server) hostPluginAllowed(id, session string) bool {
	switch id {
	case workspace.ID, serverdiag.ID:
		return s.hostToolsAllowed(session)
	default:
		return true
	}
}

// hostToolsAllowed unions the deployment default with the operator's runtime
// grants. Both are per conversation; neither can widen the other's meaning.
func (s *Server) hostToolsAllowed(session string) bool {
	if session == "" {
		return false
	}
	if slices.Contains(s.config.HostTools.Sessions, session) {
		return true
	}
	return s.hostGrants.Allows(session)
}
