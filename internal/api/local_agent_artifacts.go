package api

import (
	"crypto/sha256"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"chat-agent/internal/storage"
)

const localArtifactLimit = 32 << 20

func (s *Server) uploadLocalArtifact(w http.ResponseWriter, r *http.Request) {
	device, ok := s.localAgentAuthorized(r)
	if !ok {
		writeJSON(w, 401, map[string]string{"error": "unknown device or bad token"})
		return
	}
	op, digest := r.PathValue("operation"), r.PathValue("sha")
	if !s.localQueue.artifactUploadAllowed(op, device) || len(digest) != 64 {
		fail(w, 409, "operation cannot accept artifacts")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, localArtifactLimit+1)
	raw, err := io.ReadAll(r.Body)
	if err != nil || len(raw) > localArtifactLimit {
		fail(w, 413, "artifact is too large")
		return
	}
	hash := sha256.Sum256(raw)
	if fmt.Sprintf("%x", hash[:]) != digest {
		fail(w, 400, "artifact digest mismatch")
		return
	}
	dir := filepath.Join(s.config.DataDir, "local-agent", "artifacts", op)
	if err = os.MkdirAll(dir, 0700); err != nil {
		fail(w, 500, "artifact storage unavailable")
		return
	}
	if err = storage.Replace(filepath.Join(dir, digest), raw); err != nil {
		fail(w, 500, "artifact storage unavailable")
		return
	}
	w.WriteHeader(204)
}

func (s *Server) downloadLocalArtifact(w http.ResponseWriter, r *http.Request) {
	op, digest := r.PathValue("operation"), r.PathValue("sha")
	artifact, ok := s.localQueue.artifact(op, digest)
	if !ok {
		fail(w, 404, "文件回执不存在或已过期")
		return
	}
	path := filepath.Join(s.config.DataDir, "local-agent", "artifacts", op, digest)
	file, err := os.Open(path)
	if err != nil {
		fail(w, 404, "文件不存在")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() != artifact.Bytes {
		fail(w, 409, "文件校验失败")
		return
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": artifact.Name}))
	w.Header().Set("Content-Type", artifact.MediaType)
	w.Header().Set("ETag", `"sha256-`+digest+`"`)
	http.ServeContent(w, r, artifact.Name, info.ModTime(), file)
}
