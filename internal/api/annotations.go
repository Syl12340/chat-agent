package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"chat-agent/internal/annotations"
	"chat-agent/internal/probe"
)

// annotationStores avoids adding another lifetime field to Server while the
// plugin/runtime refactor is in flight. There is exactly one lock-bearing store
// per absolute data directory in this process.
var annotationStores sync.Map

func (s *Server) annotationStore() (*annotations.Store, error) {
	root := filepath.Join(s.config.DataDir, "annotations")
	if current, ok := annotationStores.Load(root); ok {
		return current.(*annotations.Store), nil
	}
	opened, err := annotations.Open(root)
	if err != nil {
		return nil, err
	}
	actual, _ := annotationStores.LoadOrStore(root, opened)
	return actual.(*annotations.Store), nil
}

func annotationFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, annotations.ErrNotFound):
		fail(w, http.StatusNotFound, "批注不存在")
	case errors.Is(err, annotations.ErrConflict):
		fail(w, http.StatusConflict, "批注已变化或数量已达上限，请刷新后重试")
	case errors.Is(err, annotations.ErrInvalid):
		fail(w, http.StatusBadRequest, "批注内容、来源或版本无效")
	default:
		fail(w, http.StatusServiceUnavailable, "批注存储暂时不可用")
	}
}

func (s *Server) annotationSession(w http.ResponseWriter, r *http.Request) (*probe.Session, string, bool) {
	id := r.PathValue("id")
	if !validID(id) {
		fail(w, http.StatusBadRequest, "会话标识无效")
		return nil, "", false
	}
	sess, err := s.session(id)
	if err != nil {
		fail(w, http.StatusNotFound, "会话不存在")
		return nil, "", false
	}
	return sess, sess.RoleID(), true
}

func annotationMessage(sess *probe.Session, id string) (probe.Message, bool) {
	if !validID(id) {
		return probe.Message{}, false
	}
	for _, message := range sess.Messages() {
		if message.ID == id && (message.Role == "user" || message.Role == "assistant") {
			return message, true
		}
	}
	return probe.Message{}, false
}

func (s *Server) listAnnotations(w http.ResponseWriter, r *http.Request) {
	sess, roleID, ok := s.annotationSession(w, r)
	if !ok {
		return
	}
	store, err := s.annotationStore()
	if err != nil {
		annotationFailure(w, err)
		return
	}
	items, err := store.List(r.PathValue("id"), roleID)
	if err != nil {
		annotationFailure(w, err)
		return
	}
	// A deleted/truncated message never lends its old note to a new message.
	// Keep the orphan on disk for recovery, but omit it from the live session.
	visible := items[:0]
	for _, item := range items {
		message, exists := annotationMessage(sess, item.MessageID)
		if !exists {
			continue
		}
		digest := sha256.Sum256([]byte(message.Content))
		if hex.EncodeToString(digest[:]) == item.SourceSHA256 {
			visible = append(visible, item)
		}
	}
	writeJSON(w, http.StatusOK, visible)
}

func (s *Server) createAnnotation(w http.ResponseWriter, r *http.Request) {
	sess, roleID, ok := s.annotationSession(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var input struct {
		MessageID    string `json:"message_id"`
		Start        int    `json:"start"`
		End          int    `json:"end"`
		Quote        string `json:"quote"`
		SourceSHA256 string `json:"source_sha256"`
		Note         string `json:"note"`
	}
	if !decodeInput(w, r, &input) {
		return
	}
	message, exists := annotationMessage(sess, input.MessageID)
	if !exists {
		fail(w, http.StatusConflict, "来源消息不存在或已删除")
		return
	}
	if input.Start < 0 || input.End <= input.Start || input.End > len(message.Content) || input.End-input.Start > 4096 || !utf8.ValidString(message.Content[input.Start:input.End]) {
		annotationFailure(w, annotations.ErrInvalid)
		return
	}
	quote := message.Content[input.Start:input.End]
	sourceDigest := sha256.Sum256([]byte(message.Content))
	sourceSHA := hex.EncodeToString(sourceDigest[:])
	if input.Quote != quote || !strings.EqualFold(input.SourceSHA256, sourceSHA) {
		fail(w, http.StatusConflict, "来源消息已变化，请重新选择文本")
		return
	}
	quoteDigest := sha256.Sum256([]byte(quote))
	store, err := s.annotationStore()
	if err != nil {
		annotationFailure(w, err)
		return
	}
	item, err := store.Create(annotations.Source{
		SessionID: r.PathValue("id"), RoleID: roleID, MessageID: message.ID, MessageRole: message.Role,
		Start: input.Start, End: input.End, Quote: quote, SourceSHA256: sourceSHA, QuoteSHA256: hex.EncodeToString(quoteDigest[:]),
	}, input.Note, time.Now())
	if err != nil {
		annotationFailure(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) updateAnnotation(w http.ResponseWriter, r *http.Request) {
	_, roleID, ok := s.annotationSession(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 12<<10)
	var input struct {
		Note     string `json:"note"`
		Revision int    `json:"revision"`
	}
	if !decodeInput(w, r, &input) {
		return
	}
	store, err := s.annotationStore()
	if err != nil {
		annotationFailure(w, err)
		return
	}
	item, err := store.Update(r.PathValue("id"), roleID, r.PathValue("annotation"), input.Note, input.Revision, time.Now())
	if err != nil {
		annotationFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) deleteAnnotation(w http.ResponseWriter, r *http.Request) {
	_, roleID, ok := s.annotationSession(w, r)
	if !ok {
		return
	}
	revision, err := strconv.Atoi(r.URL.Query().Get("revision"))
	if err != nil {
		annotationFailure(w, annotations.ErrInvalid)
		return
	}
	store, err := s.annotationStore()
	if err != nil {
		annotationFailure(w, err)
		return
	}
	if err = store.Delete(r.PathValue("id"), roleID, r.PathValue("annotation"), revision); err != nil {
		annotationFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
