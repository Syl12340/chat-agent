package api

import (
	"net/http"
	"sort"

	"chat-agent/internal/pluginapi"
)

// permissionView separates what a plugin asked for from what the host granted.
// The gap between them is the whole point of the policy layer, so the page shows
// both halves instead of a single "allowed" mark.
type permissionView struct {
	Name    string `json:"name"`
	Granted bool   `json:"granted"`
}

// pluginView is what a settings page needs: what the build ships, whether it
// loaded, and why not when it did not. Handlers and implementations stay inside
// the process; only names and states cross this boundary.
type pluginView struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	Version      string           `json:"version"`
	Description  string           `json:"description,omitempty"`
	Loaded       bool             `json:"loaded"`
	Reason       string           `json:"reason,omitempty"`
	Capabilities []string         `json:"capabilities,omitempty"`
	Tools        []string         `json:"tools,omitempty"`
	Permissions  []permissionView `json:"permissions,omitempty"`
}

// listPlugins reports every installed plugin, including the ones the host kept
// switched off. A capability the page cannot explain is worse than one it can.
func (s *Server) listPlugins(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"plugins": s.pluginViews()})
}

func (s *Server) pluginViews() []pluginView {
	views := []pluginView{}
	if s.plugins == nil {
		return views
	}
	toolsByPlugin := map[string][]string{}
	for _, tool := range s.plugins.Tools() {
		toolsByPlugin[tool.PluginID] = append(toolsByPlugin[tool.PluginID], tool.Tool.Name)
	}
	views = make([]pluginView, 0, len(toolsByPlugin))
	for _, manifest := range s.plugins.Plugins() {
		view := s.pluginView(manifest, true, "")
		view.Tools = toolsByPlugin[manifest.Metadata.ID]
		sort.Strings(view.Tools)
		views = append(views, view)
	}
	for _, item := range s.plugins.Unavailable() {
		if item.Manifest.Metadata.ID == "" {
			continue
		}
		views = append(views, s.pluginView(item.Manifest, false, item.Reason))
	}
	sort.SliceStable(views, func(i, j int) bool { return views[i].ID < views[j].ID })
	return views
}

func (s *Server) pluginView(manifest pluginapi.Manifest, loaded bool, reason string) pluginView {
	view := pluginView{
		ID:           manifest.Metadata.ID,
		Name:         manifest.Metadata.Name,
		Version:      manifest.Metadata.Version,
		Description:  manifest.Metadata.Description,
		Loaded:       loaded,
		Reason:       reason,
		Capabilities: append([]string(nil), manifest.Spec.Capabilities...),
	}
	for _, permission := range pluginapi.DeclaredPermissions(manifest) {
		view.Permissions = append(view.Permissions, permissionView{
			Name:    permission,
			Granted: s.pluginPolicy.Grant(manifest.Metadata.ID, permission),
		})
	}
	return view
}
