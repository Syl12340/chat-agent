package api

import (
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"strconv"
)

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
