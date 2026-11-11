package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"chat-agent/internal/presentation"
	"chat-agent/internal/storage"
)

const (
	surfaceDocumentVersion = 1
	maxSessionSurfaces     = 32
)

var (
	errSurfaceInvalid  = errors.New("invalid persistent surface")
	errSurfaceMissing  = errors.New("persistent surface not found")
	errSurfaceConflict = errors.New("persistent surface revision conflict")
	errSurfaceLimit    = errors.New("persistent surface limit reached")
)

// persistentSurface is deliberately separate from the conversation journal.
// A damaged or unsupported surface can therefore never make a legacy chat
// unreadable. The session and role binding prevents a copied file from being
// attached to another conversation.
type persistentSurface struct {
	ID        string            `json:"id"`
	Revision  int               `json:"revision"`
	View      presentation.View `json:"view"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

type surfaceDocument struct {
	Version   int                 `json:"version"`
	SessionID string              `json:"session_id"`
	RoleID    string              `json:"role_id"`
	Items     []persistentSurface `json:"items"`
}

type surfaceStore struct{ dir string }

func (s *Server) surfaceStore() surfaceStore {
	return surfaceStore{dir: filepath.Join(s.config.DataDir, "surfaces")}
}

func (s surfaceStore) path(sessionID string) string {
	return filepath.Join(s.dir, sessionID+".json")
}

func (s surfaceStore) load(sessionID, roleID string) (surfaceDocument, error) {
	doc := surfaceDocument{Version: surfaceDocumentVersion, SessionID: sessionID, RoleID: roleID, Items: []persistentSurface{}}
	raw, err := os.ReadFile(s.path(sessionID))
	if errors.Is(err, os.ErrNotExist) {
		return doc, nil
	}
	if err != nil || len(raw) > 1<<20 {
		return surfaceDocument{}, errors.New("cannot read persistent surfaces")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&doc) != nil || decoder.Decode(new(any)) != io.EOF || !validSurfaceDocument(doc, sessionID, roleID) {
		return surfaceDocument{}, errors.New("damaged persistent surfaces")
	}
	return doc, nil
}

func validSurfaceDocument(doc surfaceDocument, sessionID, roleID string) bool {
	if doc.Version != surfaceDocumentVersion || doc.SessionID != sessionID || doc.RoleID != roleID || len(doc.Items) > maxSessionSurfaces {
		return false
	}
	seen := make(map[string]bool, len(doc.Items))
	for i := range doc.Items {
		item := &doc.Items[i]
		if !validID(item.ID) || seen[item.ID] || item.Revision < 1 || item.View.ID != item.ID || item.View.Revision != item.Revision || item.CreatedAt.IsZero() || item.UpdatedAt.Before(item.CreatedAt) || !persistentViewValid(&item.View) {
			return false
		}
		seen[item.ID] = true
	}
	return true
}

func persistentViewValid(view *presentation.View) bool {
	return view != nil && validID(view.ID) && (view.Surface == presentation.Panel || view.Surface == presentation.Strip) && presentation.Validate(view) == nil
}

func (s surfaceStore) save(doc surfaceDocument) error {
	if !validSurfaceDocument(doc, doc.SessionID, doc.RoleID) {
		return errSurfaceInvalid
	}
	sort.Slice(doc.Items, func(i, j int) bool { return doc.Items[i].ID < doc.Items[j].ID })
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	return storage.Replace(s.path(doc.SessionID), raw)
}

func (s surfaceStore) removeSession(sessionID string) error {
	err := os.Remove(s.path(sessionID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func surfaceFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errSurfaceInvalid):
		fail(w, http.StatusBadRequest, "持久界面格式无效或超过预算")
	case errors.Is(err, errSurfaceMissing):
		fail(w, http.StatusNotFound, "持久界面不存在")
	case errors.Is(err, errSurfaceConflict):
		fail(w, http.StatusConflict, "持久界面已更新，请刷新后重试")
	case errors.Is(err, errSurfaceLimit):
		fail(w, http.StatusTooManyRequests, "这个会话的持久界面数量已达上限")
	default:
		fail(w, http.StatusServiceUnavailable, "持久界面暂时不可用")
	}
}

func (s *Server) surfaceSession(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	sessionID := r.PathValue("id")
	sess, ok := s.get(w, r)
	if !ok {
		return "", "", false
	}
	return sessionID, sess.RoleID(), true
}

func (s *Server) listSurfaces(w http.ResponseWriter, r *http.Request) {
	sessionID, roleID, ok := s.surfaceSession(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	doc, err := s.surfaceStore().load(sessionID, roleID)
	s.mu.Unlock()
	if err != nil {
		surfaceFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": sessionID, "role_id": roleID, "items": doc.Items})
}

func (s *Server) getSurface(w http.ResponseWriter, r *http.Request) {
	sessionID, roleID, ok := s.surfaceSession(w, r)
	if !ok {
		return
	}
	surfaceID := r.PathValue("surface")
	if !validID(surfaceID) {
		surfaceFailure(w, errSurfaceInvalid)
		return
	}
	s.mu.Lock()
	doc, err := s.surfaceStore().load(sessionID, roleID)
	s.mu.Unlock()
	if err == nil {
		for i := range doc.Items {
			if doc.Items[i].ID == surfaceID {
				writeJSON(w, http.StatusOK, doc.Items[i])
				return
			}
		}
		err = errSurfaceMissing
	}
	surfaceFailure(w, err)
}

func (s *Server) createSurface(w http.ResponseWriter, r *http.Request) {
	sessionID, roleID, ok := s.surfaceSession(w, r)
	if !ok {
		return
	}
	var input struct {
		View presentation.View `json:"view"`
	}
	if !decodeInput(w, r, &input) {
		return
	}
	input.View.Revision = 1
	if !persistentViewValid(&input.View) {
		surfaceFailure(w, errSurfaceInvalid)
		return
	}
	now := time.Now().UTC()
	item := persistentSurface{ID: input.View.ID, Revision: 1, View: input.View, CreatedAt: now, UpdatedAt: now}
	s.mu.Lock()
	doc, err := s.surfaceStore().load(sessionID, roleID)
	if err == nil {
		for i := range doc.Items {
			if doc.Items[i].ID == item.ID {
				err = errSurfaceConflict
				break
			}
		}
		if err == nil && len(doc.Items) >= maxSessionSurfaces {
			err = errSurfaceLimit
		}
	}
	if err == nil {
		doc.Items = append(doc.Items, item)
		err = s.surfaceStore().save(doc)
	}
	s.mu.Unlock()
	if err != nil {
		surfaceFailure(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) replaceSurface(w http.ResponseWriter, r *http.Request) {
	sessionID, roleID, ok := s.surfaceSession(w, r)
	if !ok {
		return
	}
	surfaceID := r.PathValue("surface")
	var input struct {
		ExpectedRevision int               `json:"expected_revision"`
		View             presentation.View `json:"view"`
	}
	if !validID(surfaceID) {
		surfaceFailure(w, errSurfaceInvalid)
		return
	}
	if !decodeInput(w, r, &input) {
		return
	}
	if input.ExpectedRevision < 1 {
		surfaceFailure(w, errSurfaceInvalid)
		return
	}
	input.View.ID = surfaceID
	input.View.Revision = input.ExpectedRevision + 1
	if !persistentViewValid(&input.View) {
		surfaceFailure(w, errSurfaceInvalid)
		return
	}
	s.mu.Lock()
	doc, err := s.surfaceStore().load(sessionID, roleID)
	var updated persistentSurface
	if err == nil {
		found := false
		for i := range doc.Items {
			if doc.Items[i].ID != surfaceID {
				continue
			}
			found = true
			if doc.Items[i].Revision != input.ExpectedRevision {
				err = errSurfaceConflict
				break
			}
			updated = doc.Items[i]
			updated.Revision++
			updated.View = input.View
			updated.UpdatedAt = time.Now().UTC()
			doc.Items[i] = updated
			break
		}
		if err == nil && !found {
			err = errSurfaceMissing
		}
	}
	if err == nil {
		err = s.surfaceStore().save(doc)
	}
	s.mu.Unlock()
	if err != nil {
		surfaceFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteSurface(w http.ResponseWriter, r *http.Request) {
	sessionID, roleID, ok := s.surfaceSession(w, r)
	if !ok {
		return
	}
	surfaceID := r.PathValue("surface")
	revision, err := strconv.Atoi(r.URL.Query().Get("revision"))
	if !validID(surfaceID) || err != nil || revision < 1 {
		surfaceFailure(w, errSurfaceInvalid)
		return
	}
	s.mu.Lock()
	doc, err := s.surfaceStore().load(sessionID, roleID)
	if err == nil {
		index := -1
		for i := range doc.Items {
			if doc.Items[i].ID == surfaceID {
				index = i
				if doc.Items[i].Revision != revision {
					err = errSurfaceConflict
				}
				break
			}
		}
		if err == nil && index < 0 {
			err = errSurfaceMissing
		}
		if err == nil {
			doc.Items = append(doc.Items[:index], doc.Items[index+1:]...)
			err = s.surfaceStore().save(doc)
		}
	}
	s.mu.Unlock()
	if err != nil {
		surfaceFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
