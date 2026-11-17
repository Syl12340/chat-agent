package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
)

func (s *Server) exportCoursewareSource(w http.ResponseWriter, r *http.Request) {
	role, ok := s.memoryRole(w, r)
	if !ok {
		return
	}
	offset, err := strconv.Atoi(r.URL.Query().Get("offset"))
	if r.URL.Query().Get("offset") == "" {
		offset, err = 0, nil
	}
	limit, limitErr := strconv.Atoi(r.URL.Query().Get("limit"))
	if r.URL.Query().Get("limit") == "" {
		limit, limitErr = 6000, nil
	}
	if err != nil || limitErr != nil || offset < 0 || limit < 500 || limit > 12000 {
		fail(w, http.StatusBadRequest, "课件导出位置无效")
		return
	}
	doc, text, source, readErr := s.courseware.Read(role, r.PathValue("document"), "", offset, limit)
	if errors.Is(readErr, os.ErrNotExist) {
		fail(w, http.StatusNotFound, "课件不存在")
		return
	}
	if readErr != nil {
		fail(w, http.StatusBadRequest, readErr.Error())
		return
	}
	ref, _ := json.Marshal(source)
	body := fmt.Sprintf("# %s — 引用原文\n\n- 文档 ID：`%s`\n- 修订：%d\n- SHA-256：`%s`\n- SourceRef：`%s`\n\n## 原文\n\n%s\n", doc.Name, doc.ID, doc.Revision, doc.SHA256, string(ref), text)
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": doc.ID + "-source.md"}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(body))
}

func (s *Server) listCourseware(w http.ResponseWriter, r *http.Request) {
	role, ok := s.memoryRole(w, r)
	if !ok {
		return
	}
	docs, err := s.courseware.List(role)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, docs)
}

func (s *Server) readCoursewareSource(w http.ResponseWriter, r *http.Request) {
	role, ok := s.memoryRole(w, r)
	if !ok {
		return
	}
	offset, err := strconv.Atoi(r.URL.Query().Get("offset"))
	if r.URL.Query().Get("offset") == "" {
		offset, err = 0, nil
	}
	limit, limitErr := strconv.Atoi(r.URL.Query().Get("limit"))
	if r.URL.Query().Get("limit") == "" {
		limit, limitErr = 6000, nil
	}
	query := strings.TrimSpace(r.URL.Query().Get("query"))
	if err != nil || limitErr != nil || offset < 0 || limit < 500 || limit > 12000 || len(query) > 256 {
		fail(w, http.StatusBadRequest, "课件原文位置无效")
		return
	}
	doc, text, source, readErr := s.courseware.Read(role, r.PathValue("document"), query, offset, limit)
	if errors.Is(readErr, os.ErrNotExist) {
		fail(w, http.StatusNotFound, "课件不存在")
		return
	}
	if readErr != nil {
		fail(w, http.StatusBadRequest, readErr.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"document": doc, "source_ref": source, "excerpt": text})
}

func (s *Server) uploadCourseware(w http.ResponseWriter, r *http.Request) {
	role, ok := s.memoryRole(w, r)
	if !ok {
		return
	}
	var input struct {
		Name      string `json:"name"`
		MediaType string `json:"media_type"`
		Data      string `json:"data_base64"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 23<<20)
	if !decodeInput(w, r, &input) {
		return
	}
	data, err := base64.StdEncoding.DecodeString(input.Data)
	if err != nil {
		fail(w, 400, "课件数据不是合法 Base64")
		return
	}
	doc, err := s.courseware.Add(role, input.Name, input.MediaType, data)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, doc)
}

func (s *Server) deleteCourseware(w http.ResponseWriter, r *http.Request) {
	role, ok := s.memoryRole(w, r)
	if !ok {
		return
	}
	revision, err := strconv.Atoi(r.URL.Query().Get("revision"))
	if err != nil || revision < 1 {
		fail(w, 400, "revision 无效")
		return
	}
	err = s.courseware.Delete(role, r.PathValue("document"), revision)
	if errors.Is(err, os.ErrNotExist) {
		fail(w, 404, "课件不存在")
		return
	}
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	w.WriteHeader(204)
}
