package companion

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"chat-agent/internal/storage"
)

var ErrActivityNotFound = errors.New("activity not found")
var ErrActivityLimit = errors.New("activity store limit reached")

// Activity records only progress the user explicitly saves. It never schedules
// messages or asks a model to infer progress from a conversation.
type Activity struct {
	ID        string    `json:"id"`
	RoleID    string    `json:"role_id"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Goal      string    `json:"goal"`
	Progress  string    `json:"progress"`
	Status    string    `json:"status"`
	Revision  int       `json:"revision"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ActivityDraft struct {
	Kind           string `json:"kind"`
	Title          string `json:"title"`
	Goal           string `json:"goal"`
	Progress       string `json:"progress"`
	IdempotencyKey string `json:"idempotency_key"`
}

type ActivityPatch struct {
	Title    *string `json:"title"`
	Goal     *string `json:"goal"`
	Progress *string `json:"progress"`
	Status   *string `json:"status"`
}

type activityRequest struct {
	RoleID     string `json:"role_id"`
	Key        string `json:"key"`
	Digest     string `json:"digest"`
	ActivityID string `json:"activity_id"`
}

type activityBinding struct {
	RoleID     string `json:"role_id"`
	ActivityID string `json:"activity_id"`
	Floor      int    `json:"history_floor"`
}

type activitySnapshot struct {
	Version    int                        `json:"version"`
	Activities []Activity                 `json:"activities"`
	Requests   map[string]activityRequest `json:"requests"`
	Bindings   map[string]activityBinding `json:"bindings"`
}

type ActivityStore struct {
	mu        sync.Mutex
	dir, lock string
	state     activitySnapshot
	closed    bool
}

func OpenActivities(dir string) (_ *ActivityStore, openErr error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock := filepath.Join(dir, ".store.lock")
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	_, err = fmt.Fprintf(f, "pid=%d\n", os.Getpid())
	err = errors.Join(err, f.Close())
	if err != nil {
		os.Remove(lock)
		return nil, err
	}
	s := &ActivityStore{dir: dir, lock: lock, state: activitySnapshot{Version: 1, Activities: []Activity{}, Requests: map[string]activityRequest{}, Bindings: map[string]activityBinding{}}}
	defer func() {
		if openErr != nil {
			s.Close()
		}
	}()
	f, err = os.Open(filepath.Join(dir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	err = errors.Join(err, f.Close())
	if err != nil {
		return nil, err
	}
	if len(b) > 4<<20 {
		return nil, ErrActivityLimit
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	var extra any
	if err = d.Decode(&s.state); err != nil {
		return nil, err
	}
	if d.Decode(&extra) != io.EOF || !validActivities(s.state) {
		return nil, errors.New("damaged activity store")
	}
	return s, nil
}

func validActivityKind(kind string) bool {
	return kind == "bedtime" || kind == "reflection" || kind == "interest" || kind == "small_step"
}

func optionalActivityText(value string, limit int) bool {
	return value == "" || textValid(value, limit, true)
}

func validActivity(a Activity) bool {
	return storage.ValidID(a.ID) && storage.ValidID(a.RoleID) && validActivityKind(a.Kind) && textValid(a.Title, 256, false) && optionalActivityText(a.Goal, 2048) && optionalActivityText(a.Progress, 4096) && (a.Status == "active" || a.Status == "paused" || a.Status == "completed") && a.Revision > 0 && !a.CreatedAt.IsZero() && !a.UpdatedAt.Before(a.CreatedAt)
}

func activityRequestKey(role, key string) string {
	digest := sha256.Sum256([]byte(role + "\x00" + key))
	return hex.EncodeToString(digest[:])
}

func validActivities(s activitySnapshot) bool {
	if s.Version != 1 || s.Activities == nil || len(s.Activities) > 200 || s.Requests == nil || len(s.Requests) > 2048 || s.Bindings == nil || len(s.Bindings) > 4096 {
		return false
	}
	ids := map[string]string{}
	for key, req := range s.Requests {
		digest, err := hex.DecodeString(req.Digest)
		if !storage.ValidID(req.RoleID) || !storage.ValidID(req.Key) || !storage.ValidID(req.ActivityID) || err != nil || len(digest) != sha256.Size || key != activityRequestKey(req.RoleID, req.Key) || ids[req.ActivityID] != "" {
			return false
		}
		ids[req.ActivityID] = req.RoleID
	}
	seen := map[string]bool{}
	counts := map[string]int{}
	for _, a := range s.Activities {
		counts[a.RoleID]++
		if !validActivity(a) || seen[a.ID] || ids[a.ID] != a.RoleID || counts[a.RoleID] > 50 {
			return false
		}
		seen[a.ID] = true
	}
	for sid, b := range s.Bindings {
		if !storage.ValidID(sid) || !storage.ValidID(b.RoleID) || !storage.ValidID(b.ActivityID) || b.Floor < 0 || ids[b.ActivityID] != b.RoleID {
			return false
		}
	}
	return true
}

func cloneActivities(s activitySnapshot) activitySnapshot {
	copy := activitySnapshot{Version: s.Version, Activities: append([]Activity{}, s.Activities...), Requests: make(map[string]activityRequest, len(s.Requests)), Bindings: make(map[string]activityBinding, len(s.Bindings))}
	for key, value := range s.Requests {
		copy.Requests[key] = value
	}
	for key, value := range s.Bindings {
		copy.Bindings[key] = value
	}
	return copy
}

func (s *ActivityStore) ready(role string) error {
	if s.closed {
		return errors.New("activity store closed")
	}
	if !storage.ValidID(role) {
		return ErrInvalid
	}
	return nil
}

func (s *ActivityStore) save(next activitySnapshot) error {
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if len(b) > 4<<20 {
		return ErrActivityLimit
	}
	if err = storage.Replace(filepath.Join(s.dir, "state.json"), b); err != nil {
		return err
	}
	s.state = next
	return nil
}

func activityIndex(items []Activity, role, id string) int {
	for i := range items {
		if items[i].ID == id && items[i].RoleID == role {
			return i
		}
	}
	return -1
}

func (s *ActivityStore) List(role string) ([]Activity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return nil, err
	}
	items := []Activity{}
	for _, a := range s.state.Activities {
		if a.RoleID == role {
			items = append(items, a)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].UpdatedAt.After(items[j].UpdatedAt) })
	return items, nil
}

func (s *ActivityStore) Get(role, id string) (Activity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return Activity{}, err
	}
	i := activityIndex(s.state.Activities, role, id)
	if i < 0 {
		return Activity{}, ErrActivityNotFound
	}
	return s.state.Activities[i], nil
}

func (s *ActivityStore) Create(role string, d ActivityDraft) (Activity, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return Activity{}, false, err
	}
	next := cloneActivities(s.state)
	a, duplicate, err := prepareActivity(&next, role, d)
	if err != nil || duplicate {
		return a, duplicate, err
	}
	if err := s.save(next); err != nil {
		return Activity{}, false, err
	}
	return a, false, nil
}

func prepareActivity(state *activitySnapshot, role string, d ActivityDraft) (Activity, bool, error) {
	d.Title, d.Goal, d.Progress = strings.TrimSpace(d.Title), strings.TrimSpace(d.Goal), strings.TrimSpace(d.Progress)
	if !storage.ValidID(d.IdempotencyKey) || !validActivityKind(d.Kind) || !textValid(d.Title, 256, false) || !optionalActivityText(d.Goal, 2048) || !optionalActivityText(d.Progress, 4096) {
		return Activity{}, false, ErrInvalid
	}
	b, _ := json.Marshal(d)
	hash := sha256.Sum256(b)
	digest := hex.EncodeToString(hash[:])
	key := activityRequestKey(role, d.IdempotencyKey)
	if req, ok := state.Requests[key]; ok {
		if req.Digest != digest {
			return Activity{}, false, ErrConflict
		}
		i := activityIndex(state.Activities, role, req.ActivityID)
		if i < 0 {
			return Activity{}, false, ErrConflict
		}
		return state.Activities[i], true, nil
	}
	count := 0
	for _, a := range state.Activities {
		if a.RoleID == role {
			count++
		}
	}
	if count >= 50 || len(state.Activities) >= 200 || len(state.Requests) >= 2048 {
		return Activity{}, false, ErrActivityLimit
	}
	id, err := lifeID()
	if err != nil {
		return Activity{}, false, err
	}
	now := time.Now().UTC()
	a := Activity{ID: id, RoleID: role, Kind: d.Kind, Title: d.Title, Goal: d.Goal, Progress: d.Progress, Status: "active", Revision: 1, CreatedAt: now, UpdatedAt: now}
	state.Activities = append(state.Activities, a)
	state.Requests[key] = activityRequest{RoleID: role, Key: d.IdempotencyKey, Digest: digest, ActivityID: id}
	return a, false, nil
}

// OpenForSession creates/resumes an activity and binds the current conversation
// in one commit. Switching preserves a floor without changing the old activity.
func (s *ActivityStore) OpenForSession(role, sessionID, id string, revision int, draft ActivityDraft, cuts map[string]int) (Activity, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return Activity{}, false, err
	}
	floor, ok := cuts[sessionID]
	if !storage.ValidID(sessionID) || !ok || floor < 0 {
		return Activity{}, false, ErrInvalid
	}
	old, bound := s.state.Bindings[sessionID]
	if bound && old.RoleID != role {
		return Activity{}, false, ErrConflict
	}
	if !bound && len(s.state.Bindings) >= 4096 {
		return Activity{}, false, ErrActivityLimit
	}
	next := cloneActivities(s.state)
	created := false
	var a Activity
	if id == "" {
		if revision != 0 {
			return Activity{}, false, ErrInvalid
		}
		item, duplicate, err := prepareActivity(&next, role, draft)
		if err != nil {
			return Activity{}, false, err
		}
		a, created = item, !duplicate
	} else {
		i := activityIndex(next.Activities, role, id)
		if i < 0 {
			return Activity{}, false, ErrActivityNotFound
		}
		a = next.Activities[i]
		if revision != a.Revision {
			return Activity{}, false, ErrConflict
		}
	}
	if bound && old.ActivityID == a.ID && a.Status == "active" {
		return a, false, nil
	}
	if a.Status != "active" {
		if err := activityCuts(&next, role, a.ID, cuts); err != nil {
			return Activity{}, false, err
		}
		a.Status = "active"
		a.Revision++
		a.UpdatedAt = time.Now().UTC()
		next.Activities[activityIndex(next.Activities, role, a.ID)] = a
	}
	if old.Floor > floor {
		floor = old.Floor
	}
	next.Bindings[sessionID] = activityBinding{RoleID: role, ActivityID: a.ID, Floor: floor}
	if err := s.save(next); err != nil {
		return Activity{}, false, err
	}
	return a, created, nil
}

// Mutation and its history barrier publish together. A reset explicitly clears a
// barrier; other changes never lower one for an existing conversation.
func activityCuts(next *activitySnapshot, role, id string, cuts map[string]int) error {
	for sid, binding := range next.Bindings {
		if binding.RoleID != role || binding.ActivityID != id {
			continue
		}
		if floor, ok := cuts[sid]; ok {
			if floor < 0 {
				return ErrInvalid
			}
			if floor > binding.Floor {
				binding.Floor = floor
				next.Bindings[sid] = binding
			}
		}
	}
	return nil
}

func (s *ActivityStore) Patch(role, id string, revision int, p ActivityPatch, cuts map[string]int) (Activity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return Activity{}, err
	}
	i := activityIndex(s.state.Activities, role, id)
	if i < 0 {
		return Activity{}, ErrActivityNotFound
	}
	a := s.state.Activities[i]
	if a.Revision != revision {
		return Activity{}, ErrConflict
	}
	if p.Title == nil && p.Goal == nil && p.Progress == nil && p.Status == nil {
		return Activity{}, ErrInvalid
	}
	if p.Title != nil {
		a.Title = strings.TrimSpace(*p.Title)
	}
	if p.Goal != nil {
		a.Goal = strings.TrimSpace(*p.Goal)
	}
	if p.Progress != nil {
		a.Progress = strings.TrimSpace(*p.Progress)
	}
	if p.Status != nil {
		a.Status = *p.Status
	}
	a.Revision++
	a.UpdatedAt = time.Now().UTC()
	if !validActivity(a) {
		return Activity{}, ErrInvalid
	}
	next := cloneActivities(s.state)
	if err := activityCuts(&next, role, id, cuts); err != nil {
		return Activity{}, err
	}
	next.Activities[i] = a
	if err := s.save(next); err != nil {
		return Activity{}, err
	}
	return a, nil
}

func (s *ActivityStore) Delete(role, id string, revision int, cuts map[string]int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return err
	}
	i := activityIndex(s.state.Activities, role, id)
	if i < 0 {
		return ErrActivityNotFound
	}
	if s.state.Activities[i].Revision != revision {
		return ErrConflict
	}
	next := cloneActivities(s.state)
	if err := activityCuts(&next, role, id, cuts); err != nil {
		return err
	}
	next.Activities = append(next.Activities[:i], next.Activities[i+1:]...)
	return s.save(next)
}

func (s *ActivityStore) Bind(role, id string, revision int, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return err
	}
	if !storage.ValidID(sessionID) {
		return ErrInvalid
	}
	i := activityIndex(s.state.Activities, role, id)
	if i < 0 {
		return ErrActivityNotFound
	}
	a := s.state.Activities[i]
	if a.Revision != revision || a.Status != "active" {
		return ErrConflict
	}
	if old, ok := s.state.Bindings[sessionID]; ok {
		if old.RoleID != role || old.ActivityID != id {
			return ErrConflict
		}
		return nil
	}
	if len(s.state.Bindings) >= 4096 {
		return ErrActivityLimit
	}
	next := cloneActivities(s.state)
	next.Bindings[sessionID] = activityBinding{RoleID: role, ActivityID: id}
	return s.save(next)
}

func (s *ActivityStore) Session(role, sessionID string) (*Activity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return nil, err
	}
	b, ok := s.state.Bindings[sessionID]
	if !ok || b.RoleID != role {
		return nil, nil
	}
	i := activityIndex(s.state.Activities, role, b.ActivityID)
	if i < 0 {
		return nil, nil
	}
	a := s.state.Activities[i]
	return &a, nil
}

func (s *ActivityStore) Context(role, sessionID string) (*Activity, error) {
	a, err := s.Session(role, sessionID)
	if a != nil && a.Status != "active" {
		a = nil
	}
	return a, err
}

func (s *ActivityStore) Floor(role, sessionID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return 0, err
	}
	if b, ok := s.state.Bindings[sessionID]; ok && b.RoleID == role {
		return b.Floor, nil
	}
	return 0, nil
}

func (s *ActivityStore) ResetFloor(role, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return err
	}
	b, ok := s.state.Bindings[sessionID]
	if !ok || b.RoleID != role || b.Floor == 0 {
		return nil
	}
	next := cloneActivities(s.state)
	b.Floor = 0
	next.Bindings[sessionID] = b
	return s.save(next)
}

func (s *ActivityStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return os.Remove(s.lock)
}
