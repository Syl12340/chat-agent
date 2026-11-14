package api

import (
	"encoding/json"
	"log"
	"net/http"
	"sort"

	serverdiag "chat-agent/plugins/builtin/server"
	"chat-agent/plugins/builtin/workspace"
)

// hostToolState tells the page what it may offer: whether this deployment has
// host tooling at all, which conversations already have it, and which of those
// come from the environment rather than from a runtime grant.
func (s *Server) hostToolState(w http.ResponseWriter, r *http.Request) {
	if !requireDeviceAdmin(w, r) {
		return
	}
	writeJSON(w, 200, map[string]any{
		"configured":   s.hostToolingConfigured(),
		"tools":        s.hostToolNames(),
		"sessions":     s.hostGrants.List(),
		"env_sessions": append([]string{}, s.config.HostTools.Sessions...),
		"limit":        maxHostGrantSessions,
	})
}

// updateHostGrants is the only way a conversation gains host tools without a
// restart. It is deliberately not a model tool: the assistant can neither grant
// itself machine access nor widen the session list, and the ticket that
// authorises a maintenance action stays a separate, single-use decision.
func (s *Server) updateHostGrants(w http.ResponseWriter, r *http.Request) {
	if !requireDeviceAdmin(w, r) {
		return
	}
	var body struct {
		Session string `json:"session"`
		Allow   *bool  `json:"allow"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil || body.Allow == nil {
		fail(w, 400, "请求格式不正确")
		return
	}
	if !validID(body.Session) {
		fail(w, 400, "会话标识不正确")
		return
	}
	if _, err := s.session(body.Session); err != nil {
		fail(w, 404, "会话不存在")
		return
	}
	if err := s.hostGrants.Set(body.Session, *body.Allow); err != nil {
		// The page only shows the switch after a successful save, so refusing here
		// keeps what the operator sees equal to what the server will honour.
		fail(w, 503, "运维授权没有保存")
		return
	}
	// Machine access changing is worth a line an operator can find later.
	if *body.Allow {
		log.Printf("host tools granted to session %s", body.Session)
	} else {
		log.Printf("host tools revoked from session %s", body.Session)
	}
	s.hostToolState(w, r)
}

// hostToolingConfigured reports whether this deployment can look at the machine
// at all. It is independent of who is allowed to see the result.
func (s *Server) hostToolingConfigured() bool {
	if s.plugins == nil {
		return false
	}
	for _, manifest := range s.plugins.Plugins() {
		if manifest.Metadata.ID == serverdiag.ID || manifest.Metadata.ID == workspace.ID {
			return true
		}
	}
	return false
}

func (s *Server) hostToolNames() []string {
	names := []string{}
	if s.plugins == nil {
		return names
	}
	for _, loaded := range s.plugins.Tools() {
		if loaded.PluginID == serverdiag.ID || loaded.PluginID == workspace.ID {
			names = append(names, loaded.Tool.Name)
		}
	}
	sort.Strings(names)
	return names
}
