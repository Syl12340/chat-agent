package api

import (
	"mime"
	"net/http"
	"path/filepath"

	"chat-agent/plugins/builtin/workspace"
)

func (s *Server) workspaceFile(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.get(w, r); !ok {
		return
	}
	session := r.PathValue("id")
	if !s.hostPluginAllowed(workspace.ID, session) || s.config.HostTools.WorkspaceRoot == "" || !s.pluginPolicy.PluginEnabled(workspace.ID) || !s.pluginPolicy.Grant(workspace.ID, workspace.Permission) {
		fail(w, http.StatusForbidden, "这个会话没有服务器工作区权限")
		return
	}
	digest := r.PathValue("sha")
	path := r.URL.Query().Get("path")
	file, info, mediaType, _, err := workspace.OpenDownload(s.config.HostTools.WorkspaceRoot, path, digest)
	if err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	defer file.Close()
	name := filepath.Base(path)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("ETag", `"sha256-`+digest+`"`)
	http.ServeContent(w, r, name, info.ModTime(), file)
}
