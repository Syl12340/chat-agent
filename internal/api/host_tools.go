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
	if len(c.HostTools.Sessions) == 0 {
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
		return slices.Contains(s.config.HostTools.Sessions, session)
	default:
		return true
	}
}
