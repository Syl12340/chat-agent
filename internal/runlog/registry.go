package runlog

import (
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

	"chat-agent/internal/probe"
	"chat-agent/internal/storage"
	"chat-agent/internal/turns"
)

const registryLimit = 100_000

var ErrMaintenance = errors.New("runtime session requires explicit maintenance")

// Manifest is the only generation selector. Readers never scan for a newer
// directory and never fall back to transcript-era files. The layout and switch
// ordering are adapted from DeepSeek Harness (MIT); the implementation is
// native Go and keeps chat-agent's own authorization boundaries.
type Manifest struct {
	Version      int    `json:"version"`
	Contract     string `json:"contract"`
	SessionID    string `json:"session_id"`
	RoleID       string `json:"role_id"`
	Generation   int    `json:"generation"`
	ImportedFrom string `json:"imported_from"`
}

type IndexEntry struct {
	RoleID       string    `json:"role_id"`
	Generation   int       `json:"generation"`
	ImportedFrom string    `json:"imported_from"`
	State        string    `json:"state"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type formatIndex struct {
	Version  int                   `json:"version"`
	Contract string                `json:"contract"`
	Sessions map[string]IndexEntry `json:"sessions"`
}

type Deletion struct {
	ID             string     `json:"id"`
	Sequence       uint64     `json:"sequence"`
	SessionID      string     `json:"session_id"`
	Scope          string     `json:"scope"`
	PrimaryRequest string     `json:"primary_request,omitempty"`
	RequestIDs     []string   `json:"request_ids,omitempty"`
	MessageStart   int        `json:"message_start,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
}

type deletionLedger struct {
	Version      int        `json:"version"`
	Contract     string     `json:"contract"`
	NextSequence uint64     `json:"next_sequence"`
	Items        []Deletion `json:"items"`
}

type SessionEntry struct {
	ID        string
	RoleID    string
	UpdatedAt time.Time
}

type MigrationReport struct {
	Migrated int `json:"migrated"`
	Resumed  int `json:"resumed"`
	Skipped  int `json:"skipped"`
}

type Registry struct {
	mu         sync.Mutex
	root       string
	lockPath   string
	index      formatIndex
	deletions  deletionLedger
	closed     bool
	switchHook func(stage string) error
}

func OpenRegistry(root string) (_ *Registry, openErr error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(root, ".registry.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("runtime registry unavailable: %w", err)
	}
	_, writeErr := fmt.Fprintf(lock, "pid=%d\n", os.Getpid())
	if err = errors.Join(writeErr, lock.Close()); err != nil {
		_ = os.Remove(lockPath)
		return nil, err
	}
	r := &Registry{root: root, lockPath: lockPath}
	defer func() {
		if openErr != nil {
			_ = r.Close()
		}
	}()
	if err = r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Registry) load() error {
	r.index = formatIndex{Version: Version, Contract: Contract, Sessions: map[string]IndexEntry{}}
	r.deletions = deletionLedger{Version: Version, Contract: Contract, NextSequence: 1, Items: []Deletion{}}
	if err := loadOrCreate(filepath.Join(r.root, "format-index.json"), &r.index); err != nil {
		return fmt.Errorf("load runtime format index: %w", err)
	}
	if err := loadOrCreate(filepath.Join(r.root, "deletions.json"), &r.deletions); err != nil {
		return fmt.Errorf("load runtime deletion ledger: %w", err)
	}
	if r.index.Version != Version || r.index.Contract != Contract || r.index.Sessions == nil || len(r.index.Sessions) > registryLimit {
		return errors.New("invalid runtime format index")
	}
	for id, entry := range r.index.Sessions {
		if !storage.ValidID(id) || !storage.ValidID(entry.RoleID) || entry.Generation < 1 || entry.ImportedFrom == "" || entry.UpdatedAt.IsZero() || entry.State != "active" && entry.State != "maintenance" && entry.State != "deleted" {
			return errors.New("invalid runtime format index entry")
		}
	}
	if r.deletions.Version != Version || r.deletions.Contract != Contract || r.deletions.NextSequence < 1 || len(r.deletions.Items) > registryLimit {
		return errors.New("invalid runtime deletion ledger")
	}
	var previous uint64
	seen := map[string]bool{}
	for _, item := range r.deletions.Items {
		if !storage.ValidID(item.ID) || seen[item.ID] || !storage.ValidID(item.SessionID) || item.Sequence <= previous || item.Sequence >= r.deletions.NextSequence || item.CreatedAt.IsZero() || item.Scope != "session" && item.Scope != "turn" || item.Scope == "turn" && (!storage.ValidID(item.PrimaryRequest) || len(item.RequestIDs) == 0 || item.MessageStart < 0) {
			return errors.New("invalid runtime deletion entry")
		}
		for _, requestID := range item.RequestIDs {
			if !storage.ValidID(requestID) {
				return errors.New("invalid runtime deletion request")
			}
		}
		seen[item.ID] = true
		previous = item.Sequence
	}
	return nil
}

func loadOrCreate(path string, value any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return saveJSON(path, value)
	}
	if err != nil {
		return err
	}
	if len(b) > 32<<20 {
		return errors.New("registry file exceeds capacity")
	}
	return decodeStrict(b, value)
}

func saveJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if len(b) > 32<<20 {
		return errors.New("registry file exceeds capacity")
	}
	return storage.Replace(path, append(b, '\n'))
}

func (r *Registry) Root() string { return r.root }

// AdoptParentOwnership makes a stopped-server maintenance run leave the new
// runtime tree writable by the same account that owns the configured data
// directory. It is a no-op when ownership already matches and on Windows.
func (r *Registry) AdoptParentOwnership() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.readyLocked(); err != nil {
		return err
	}
	return matchTreeOwnership(r.root, filepath.Dir(r.root))
}

func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return os.Remove(r.lockPath)
}

func (r *Registry) readyLocked() error {
	if r.closed {
		return errors.New("runtime registry closed")
	}
	return nil
}

func generationName(value int) string { return fmt.Sprintf("g-%d", value) }

func (r *Registry) manifestLocked(id string, entry IndexEntry) (Manifest, error) {
	b, err := os.ReadFile(filepath.Join(r.root, id, "manifest.json"))
	if err != nil {
		return Manifest{}, errors.Join(ErrMaintenance, err)
	}
	var manifest Manifest
	if len(b) > 64<<10 || decodeStrict(b, &manifest) != nil || manifest.Version != Version || manifest.Contract != Contract || manifest.SessionID != id || manifest.RoleID != entry.RoleID || manifest.Generation != entry.Generation || manifest.ImportedFrom != entry.ImportedFrom {
		return Manifest{}, ErrMaintenance
	}
	return manifest, nil
}

func (r *Registry) hasPendingLocked(id string) bool {
	for i := range r.deletions.Items {
		if r.deletions.Items[i].SessionID == id && r.deletions.Items[i].CompletedAt == nil {
			return true
		}
	}
	return false
}

func (r *Registry) projectionPathLocked(id, name string) (string, error) {
	if err := r.readyLocked(); err != nil {
		return "", err
	}
	entry, ok := r.index.Sessions[id]
	if !ok || entry.State == "deleted" {
		return "", os.ErrNotExist
	}
	if entry.State != "active" || r.hasPendingLocked(id) {
		return "", ErrMaintenance
	}
	if _, err := r.manifestLocked(id, entry); err != nil {
		return "", err
	}
	path := filepath.Join(r.root, id, generationName(entry.Generation), name)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.Join(ErrMaintenance, err)
	}
	return path, nil
}

func (r *Registry) HistoryPath(id string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.projectionPathLocked(id, "history.json")
}

func (r *Registry) TurnsPath(id string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.projectionPathLocked(id, "turns.json")
}

func (r *Registry) Entries() ([]SessionEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.readyLocked(); err != nil {
		return nil, err
	}
	out := []SessionEntry{}
	for id, entry := range r.index.Sessions {
		if entry.State == "deleted" {
			continue
		}
		if entry.State != "active" || r.hasPendingLocked(id) {
			out = append(out, SessionEntry{ID: id, RoleID: entry.RoleID, UpdatedAt: entry.UpdatedAt})
			continue
		}
		if _, err := r.manifestLocked(id, entry); err != nil {
			out = append(out, SessionEntry{ID: id, RoleID: entry.RoleID, UpdatedAt: entry.UpdatedAt})
			continue
		}
		info, err := os.Stat(filepath.Join(r.root, id, generationName(entry.Generation), "history.json"))
		if err == nil {
			out = append(out, SessionEntry{ID: id, RoleID: entry.RoleID, UpdatedAt: info.ModTime().UTC()})
		} else {
			out = append(out, SessionEntry{ID: id, RoleID: entry.RoleID, UpdatedAt: entry.UpdatedAt})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func initialHistory(roleID string) ([]byte, error) {
	return json.Marshal(struct {
		Version int    `json:"version"`
		RoleID  string `json:"role_id"`
	}{Version: 2, RoleID: roleID})
}

func (r *Registry) Create(id, roleID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.readyLocked(); err != nil {
		return err
	}
	if !storage.ValidID(id) || !storage.ValidID(roleID) {
		return errors.New("invalid runtime session identity")
	}
	if _, exists := r.index.Sessions[id]; exists || len(r.index.Sessions) >= registryLimit {
		return errors.New("runtime session identity is unavailable")
	}
	history, _ := initialHistory(roleID)
	history = append(history, '\n')
	turnData, err := turns.EmptyProjection(id)
	if err != nil {
		return err
	}
	_, err = r.publishLocked(id, roleID, "new-runtime-v2", history, turnData, nil)
	return err
}

func (r *Registry) publishLocked(id, roleID, importedFrom string, history, turnData []byte, transform func(string) error) (int, error) {
	if len(history) == 0 || len(history) > MaxGenerationSize || len(turnData) == 0 || len(turnData) > 12<<20 {
		return 0, errors.New("runtime projection exceeds capacity")
	}
	generation := 1
	if current, ok := r.index.Sessions[id]; ok {
		generation = current.Generation + 1
	}
	sessionDir := filepath.Join(r.root, id)
	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		return 0, err
	}
	token, err := randomID()
	if err != nil {
		return 0, err
	}
	candidate := filepath.Join(sessionDir, fmt.Sprintf(".%s.candidate-%s", generationName(generation), token))
	if err = os.Mkdir(candidate, 0700); err != nil {
		return 0, err
	}
	keepCandidate := false
	defer func() {
		if !keepCandidate {
			_ = os.RemoveAll(candidate)
		}
	}()
	for _, dir := range []string{filepath.Join(candidate, "snapshots"), filepath.Join(candidate, "payloads")} {
		if err = os.Mkdir(dir, 0700); err != nil {
			return 0, err
		}
	}
	if err = storage.Replace(filepath.Join(candidate, "history.json"), history); err != nil {
		return 0, err
	}
	if normalized, normalizeErr := turns.NormalizeLegacy(id, turnData); normalizeErr != nil {
		return 0, normalizeErr
	} else if err = storage.Replace(filepath.Join(candidate, "turns.json"), normalized); err != nil {
		return 0, err
	}
	if err = storage.Replace(filepath.Join(candidate, "events.jsonl"), nil); err != nil {
		return 0, err
	}
	if transform != nil {
		if err = transform(candidate); err != nil {
			return 0, err
		}
	}
	sess, err := probe.OpenRuntimeSession(filepath.Join(candidate, "history.json"))
	if err != nil {
		return 0, err
	}
	actualRole := sess.RoleID()
	closeErr := sess.Close()
	if closeErr != nil || actualRole != roleID {
		return 0, errors.Join(errors.New("runtime history role mismatch"), closeErr)
	}
	turnBytes, err := os.ReadFile(filepath.Join(candidate, "turns.json"))
	if err != nil {
		return 0, err
	}
	if _, err = turns.NormalizeLegacy(id, turnBytes); err != nil {
		return 0, err
	}
	if used, sizeErr := directorySize(candidate); sizeErr != nil || used > MaxGenerationSize {
		if sizeErr == nil {
			sizeErr = errors.New("runtime generation exceeds capacity")
		}
		return 0, sizeErr
	}
	finalDir := filepath.Join(sessionDir, generationName(generation))
	if err = os.Rename(candidate, finalDir); err != nil {
		return 0, err
	}
	keepCandidate = true
	entry := IndexEntry{RoleID: roleID, Generation: generation, ImportedFrom: importedFrom, State: "maintenance", UpdatedAt: time.Now().UTC()}
	r.index.Sessions[id] = entry
	if err = saveJSON(filepath.Join(r.root, "format-index.json"), &r.index); err != nil {
		return 0, err
	}
	if r.switchHook != nil {
		if err = r.switchHook("after-index"); err != nil {
			return 0, err
		}
	}
	manifest := Manifest{Version: Version, Contract: Contract, SessionID: id, RoleID: roleID, Generation: generation, ImportedFrom: importedFrom}
	if err = saveJSON(filepath.Join(sessionDir, "manifest.json"), manifest); err != nil {
		return 0, err
	}
	if r.switchHook != nil {
		if err = r.switchHook("after-manifest"); err != nil {
			return 0, err
		}
	}
	entry.State = "active"
	entry.UpdatedAt = time.Now().UTC()
	r.index.Sessions[id] = entry
	if err = saveJSON(filepath.Join(r.root, "format-index.json"), &r.index); err != nil {
		entry.State = "maintenance"
		r.index.Sessions[id] = entry
		return 0, err
	}
	return generation, nil
}

func (r *Registry) resumeLocked(id string, entry IndexEntry) error {
	if entry.State != "maintenance" {
		return nil
	}
	dir := filepath.Join(r.root, id, generationName(entry.Generation))
	for _, name := range []string{"history.json", "turns.json", "events.jsonl"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return errors.Join(ErrMaintenance, err)
		}
	}
	sess, err := probe.OpenRuntimeSession(filepath.Join(dir, "history.json"))
	if err != nil {
		return err
	}
	roleID := sess.RoleID()
	if closeErr := sess.Close(); closeErr != nil || roleID != entry.RoleID {
		return errors.Join(errors.New("runtime history role mismatch"), closeErr)
	}
	turnData, err := os.ReadFile(filepath.Join(dir, "turns.json"))
	if err != nil {
		return err
	}
	if _, err = turns.NormalizeLegacy(id, turnData); err != nil {
		return err
	}
	manifest := Manifest{Version: Version, Contract: Contract, SessionID: id, RoleID: entry.RoleID, Generation: entry.Generation, ImportedFrom: entry.ImportedFrom}
	if err = saveJSON(filepath.Join(r.root, id, "manifest.json"), manifest); err != nil {
		return err
	}
	entry.State = "active"
	entry.UpdatedAt = time.Now().UTC()
	r.index.Sessions[id] = entry
	if err = saveJSON(filepath.Join(r.root, "format-index.json"), &r.index); err != nil {
		entry.State = "maintenance"
		r.index.Sessions[id] = entry
		return err
	}
	return nil
}

func (r *Registry) requireNoLegacyLocked(dataRoot string) error {
	dir := filepath.Join(dataRoot, probe.AgentID)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, file := range entries {
		if file.Type().IsRegular() && strings.HasSuffix(file.Name(), ".jsonl") {
			id := strings.TrimSuffix(file.Name(), ".jsonl")
			entry, ok := r.index.Sessions[id]
			if !ok || entry.State == "maintenance" {
				return errors.New("legacy sessions require -migrate-runtime before server start")
			}
		}
	}
	return nil
}

func (r *Registry) RequireMigrated(dataRoot string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.readyLocked(); err != nil {
		return err
	}
	if r.hasAnyPendingLocked() {
		return ErrMaintenance
	}
	return r.requireNoLegacyLocked(dataRoot)
}

func (r *Registry) hasAnyPendingLocked() bool {
	for i := range r.deletions.Items {
		if r.deletions.Items[i].CompletedAt == nil {
			return true
		}
	}
	return false
}

func legacyFiles(dataRoot, id string) (string, string) {
	return filepath.Join(dataRoot, probe.AgentID, id+".jsonl"), filepath.Join(dataRoot, "turns", id+".json")
}

func (r *Registry) cleanupLegacyLocked(dataRoot, id string) error {
	history, ledger := legacyFiles(dataRoot, id)
	var out error
	for _, path := range []string{history, strings.TrimSuffix(history, ".jsonl") + ".lock", ledger} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			out = errors.Join(out, err)
		}
	}
	return out
}

func (r *Registry) MigrateLegacy(dataRoot string) (MigrationReport, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var report MigrationReport
	if err := r.readyLocked(); err != nil {
		return report, err
	}
	if _, err := os.Stat(filepath.Join(dataRoot, "turns", ".store.lock")); err == nil {
		return report, errors.New("legacy turn store is locked; stop the server before migration")
	} else if !errors.Is(err, os.ErrNotExist) {
		return report, err
	}
	if err := r.repairPendingLocked(dataRoot); err != nil {
		return report, err
	}
	for id, entry := range r.index.Sessions {
		if entry.State == "maintenance" {
			if err := r.resumeLocked(id, entry); err != nil {
				return report, err
			}
			report.Resumed++
		}
	}
	dir := filepath.Join(dataRoot, probe.AgentID)
	files, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return report, nil
	}
	if err != nil {
		return report, err
	}
	for _, file := range files {
		if !file.Type().IsRegular() || !strings.HasSuffix(file.Name(), ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(file.Name(), ".jsonl")
		if !storage.ValidID(id) {
			return report, errors.New("invalid legacy session filename")
		}
		if entry, ok := r.index.Sessions[id]; ok {
			if entry.State == "active" || entry.State == "deleted" {
				if err = r.cleanupLegacyLocked(dataRoot, id); err != nil {
					return report, err
				}
				report.Skipped++
				continue
			}
		}
		historyPath, turnPath := legacyFiles(dataRoot, id)
		sess, openErr := probe.OpenSession(dataRoot, probe.AgentID, id)
		if openErr != nil {
			return report, fmt.Errorf("validate legacy session %s: %w", id, openErr)
		}
		roleID := sess.RoleID()
		if closeErr := sess.Close(); closeErr != nil {
			return report, closeErr
		}
		history, readErr := os.ReadFile(historyPath)
		if readErr != nil {
			return report, readErr
		}
		turnData, readErr := os.ReadFile(turnPath)
		if errors.Is(readErr, os.ErrNotExist) {
			turnData, readErr = turns.EmptyProjection(id)
		}
		if readErr != nil {
			return report, readErr
		}
		turnData, err = turns.NormalizeLegacy(id, turnData)
		if err != nil {
			return report, fmt.Errorf("validate legacy turns %s: %w", id, err)
		}
		if _, err = r.publishLocked(id, roleID, "transcript-v1-v2+turns-v1-v3", history, turnData, nil); err != nil {
			return report, err
		}
		if err = r.cleanupLegacyLocked(dataRoot, id); err != nil {
			return report, err
		}
		report.Migrated++
	}
	turnFiles, readErr := os.ReadDir(filepath.Join(dataRoot, "turns"))
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return report, readErr
	}
	for _, file := range turnFiles {
		if !file.Type().IsRegular() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(file.Name(), ".json")
		entry, ok := r.index.Sessions[id]
		if !storage.ValidID(id) || !ok || entry.State == "maintenance" {
			return report, fmt.Errorf("orphan legacy turn ledger %q requires explicit recovery", file.Name())
		}
		if err = os.Remove(filepath.Join(dataRoot, "turns", file.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return report, err
		}
	}
	return report, nil
}

func (r *Registry) addDeletionLocked(item Deletion) (string, error) {
	if len(r.deletions.Items) >= registryLimit {
		return "", errors.New("runtime deletion ledger is full")
	}
	id, err := randomID()
	if err != nil {
		return "", err
	}
	item.ID = id
	item.Sequence = r.deletions.NextSequence
	item.CreatedAt = time.Now().UTC()
	r.deletions.NextSequence++
	r.deletions.Items = append(r.deletions.Items, item)
	if err = saveJSON(filepath.Join(r.root, "deletions.json"), &r.deletions); err != nil {
		r.deletions.Items = r.deletions.Items[:len(r.deletions.Items)-1]
		r.deletions.NextSequence--
		return "", err
	}
	return id, nil
}

func (r *Registry) completeDeletionLocked(id string) error {
	for i := range r.deletions.Items {
		item := &r.deletions.Items[i]
		if item.ID == id {
			if item.CompletedAt != nil {
				return nil
			}
			now := time.Now().UTC()
			item.CompletedAt = &now
			if err := saveJSON(filepath.Join(r.root, "deletions.json"), &r.deletions); err != nil {
				item.CompletedAt = nil
				return err
			}
			return nil
		}
	}
	return errors.New("runtime deletion not found")
}

func (r *Registry) RewriteLatestTurn(id, primaryRequest string, messageStart int) ([]string, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.readyLocked(); err != nil {
		return nil, "", err
	}
	if r.hasPendingLocked(id) {
		return nil, "", ErrMaintenance
	}
	entry, ok := r.index.Sessions[id]
	if !ok || entry.State != "active" {
		return nil, "", os.ErrNotExist
	}
	if _, err := r.manifestLocked(id, entry); err != nil {
		return nil, "", err
	}
	active := filepath.Join(r.root, id, generationName(entry.Generation))
	history, err := os.ReadFile(filepath.Join(active, "history.json"))
	if err != nil {
		return nil, "", err
	}
	turnData, err := os.ReadFile(filepath.Join(active, "turns.json"))
	if err != nil {
		return nil, "", err
	}
	nextTurns, removed, err := turns.RemoveBatchProjection(id, primaryRequest, turnData)
	if err != nil {
		return nil, "", err
	}
	deleteID, err := r.addDeletionLocked(Deletion{SessionID: id, Scope: "turn", PrimaryRequest: primaryRequest, RequestIDs: append([]string(nil), removed...), MessageStart: messageStart})
	if err != nil {
		return nil, "", err
	}
	newGeneration, err := r.publishLocked(id, entry.RoleID, "turn-delete-rewrite", history, nextTurns, func(candidate string) error {
		sess, openErr := probe.OpenRuntimeSession(filepath.Join(candidate, "history.json"))
		if openErr != nil {
			return openErr
		}
		total := len(sess.Messages())
		if messageStart < 0 || messageStart >= total {
			_ = sess.Close()
			return errors.New("turn history boundary is invalid")
		}
		_, dropErr := sess.DropTail(total - messageStart)
		return errors.Join(dropErr, sess.Close())
	})
	if err != nil {
		return nil, deleteID, err
	}
	if err = r.removeOtherGenerationsLocked(id, newGeneration); err != nil {
		return nil, deleteID, err
	}
	if err = r.completeDeletionLocked(deleteID); err != nil {
		return nil, deleteID, err
	}
	return removed, deleteID, nil
}

func (r *Registry) removeOtherGenerationsLocked(id string, keep int) error {
	dir := filepath.Join(r.root, id)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != generationName(keep) {
			if strings.HasPrefix(entry.Name(), "g-") || strings.HasPrefix(entry.Name(), ".g-") {
				if err = os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (r *Registry) BeginSessionDeletion(id string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.readyLocked(); err != nil {
		return "", err
	}
	for i := range r.deletions.Items {
		item := &r.deletions.Items[i]
		if item.SessionID == id && item.CompletedAt == nil {
			if item.Scope == "session" {
				return item.ID, nil
			}
			return "", ErrMaintenance
		}
	}
	entry, ok := r.index.Sessions[id]
	if !ok || entry.State == "deleted" {
		return "", os.ErrNotExist
	}
	if entry.State != "active" {
		return "", ErrMaintenance
	}
	return r.addDeletionLocked(Deletion{SessionID: id, Scope: "session"})
}

func (r *Registry) RemoveSessionData(dataRoot, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.index.Sessions[id]
	if !ok {
		return os.ErrNotExist
	}
	previous := entry
	entry.State = "deleted"
	entry.UpdatedAt = time.Now().UTC()
	r.index.Sessions[id] = entry
	if err := saveJSON(filepath.Join(r.root, "format-index.json"), &r.index); err != nil {
		r.index.Sessions[id] = previous
		return err
	}
	if err := os.RemoveAll(filepath.Join(r.root, id)); err != nil {
		return err
	}
	return r.cleanupLegacyLocked(dataRoot, id)
}

func (r *Registry) CompleteDeletion(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.completeDeletionLocked(id)
}

// ExportLegacy writes a separate transcript-v2/turns-v3 conversation export
// for an explicitly selected older binary. Runtime events and unknown
// settlement facts stay in the source runtime archive because the old formats
// cannot represent them. The destination must not already exist.
func (r *Registry) ExportLegacy(dataRoot, destination string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.readyLocked(); err != nil {
		return err
	}
	if r.hasAnyPendingLocked() {
		return ErrMaintenance
	}
	if _, err := os.Stat(filepath.Join(dataRoot, "turns", ".store.lock")); err == nil {
		return errors.New("turn store is locked; stop the server before export")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	destination, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if _, err = os.Stat(destination); err == nil {
		return errors.New("export destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(destination)
	if err = os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".chat-agent-export-")
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(staging)
		}
	}()
	for _, name := range []string{probe.AgentID, "turns", "images"} {
		if err = os.Mkdir(filepath.Join(staging, name), 0700); err != nil {
			return err
		}
	}
	count := 0
	for id, entry := range r.index.Sessions {
		if entry.State == "deleted" {
			continue
		}
		if entry.State != "active" {
			return ErrMaintenance
		}
		if _, err = r.manifestLocked(id, entry); err != nil {
			return err
		}
		generation := filepath.Join(r.root, id, generationName(entry.Generation))
		history, readErr := os.ReadFile(filepath.Join(generation, "history.json"))
		if readErr != nil {
			return readErr
		}
		if err = storage.Replace(filepath.Join(staging, probe.AgentID, id+".jsonl"), history); err != nil {
			return err
		}
		turnData, readErr := os.ReadFile(filepath.Join(generation, "turns.json"))
		if readErr != nil {
			return readErr
		}
		if turnData, readErr = turns.ExportV3(id, turnData); readErr != nil {
			return readErr
		}
		if err = storage.Replace(filepath.Join(staging, "turns", id+".json"), turnData); err != nil {
			return err
		}
		imageSource := filepath.Join(dataRoot, "images", id)
		if _, statErr := os.Stat(imageSource); statErr == nil {
			if err = copyTree(imageSource, filepath.Join(staging, "images", id), 256<<20); err != nil {
				return err
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		count++
	}
	metadata := struct {
		Version   int       `json:"version"`
		Contract  string    `json:"contract"`
		CreatedAt time.Time `json:"created_at"`
		Sessions  int       `json:"sessions"`
		Notice    string    `json:"notice"`
	}{Version: Version, Contract: Contract, CreatedAt: time.Now().UTC(), Sessions: count, Notice: "conversation compatibility export; runtime events remain in the source archive"}
	if err = saveJSON(filepath.Join(staging, "runtime-export.json"), metadata); err != nil {
		return err
	}
	if err = os.Rename(staging, destination); err != nil {
		return err
	}
	published = true
	return nil
}

func copyTree(source, destination string, limit int64) error {
	var used int64
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("export source contains a symbolic link")
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("invalid export source path")
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		if !entry.Type().IsRegular() {
			return errors.New("export source contains a non-regular file")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		used += info.Size()
		if used > limit {
			return errors.New("export image data exceeds capacity")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return storage.Replace(target, data)
	})
}

// RepairPending completes only deletion work already recorded in the
// non-content ledger. It is intentionally explicit and is called by the
// stopped-server migration command, never during normal server startup.
func (r *Registry) RepairPending(dataRoot string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.readyLocked(); err != nil {
		return err
	}
	return r.repairPendingLocked(dataRoot)
}

func (r *Registry) repairPendingLocked(dataRoot string) error {
	for i := range r.deletions.Items {
		item := &r.deletions.Items[i]
		if item.CompletedAt != nil {
			continue
		}
		entry, ok := r.index.Sessions[item.SessionID]
		if !ok {
			return ErrMaintenance
		}
		if item.Scope == "session" {
			previous := entry
			entry.State = "deleted"
			entry.UpdatedAt = time.Now().UTC()
			r.index.Sessions[item.SessionID] = entry
			if err := saveJSON(filepath.Join(r.root, "format-index.json"), &r.index); err != nil {
				r.index.Sessions[item.SessionID] = previous
				return err
			}
			if err := os.RemoveAll(filepath.Join(r.root, item.SessionID)); err != nil {
				return err
			}
			if err := r.cleanupLegacyLocked(dataRoot, item.SessionID); err != nil {
				return err
			}
			// A crash may have occurred before the online image store cleanup.
			// The target is an exact validated session child of the data root.
			if err := os.RemoveAll(filepath.Join(dataRoot, "images", item.SessionID)); err != nil {
				return err
			}
			if err := r.completeDeletionLocked(item.ID); err != nil {
				return err
			}
			continue
		}
		if entry.State == "maintenance" {
			if err := r.resumeLocked(item.SessionID, entry); err != nil {
				return err
			}
			entry = r.index.Sessions[item.SessionID]
		}
		if entry.State != "active" {
			return ErrMaintenance
		}
		active := filepath.Join(r.root, item.SessionID, generationName(entry.Generation))
		turnData, err := os.ReadFile(filepath.Join(active, "turns.json"))
		if err != nil {
			return err
		}
		nextTurns, _, removeErr := turns.RemoveBatchProjection(item.SessionID, item.PrimaryRequest, turnData)
		if errors.Is(removeErr, turns.ErrNotFound) {
			if err = r.removeOtherGenerationsLocked(item.SessionID, entry.Generation); err != nil {
				return err
			}
			if err = r.completeDeletionLocked(item.ID); err != nil {
				return err
			}
			continue
		}
		if removeErr != nil {
			return removeErr
		}
		history, err := os.ReadFile(filepath.Join(active, "history.json"))
		if err != nil {
			return err
		}
		generation, err := r.publishLocked(item.SessionID, entry.RoleID, "turn-delete-repair", history, nextTurns, func(candidate string) error {
			sess, openErr := probe.OpenRuntimeSession(filepath.Join(candidate, "history.json"))
			if openErr != nil {
				return openErr
			}
			total := len(sess.Messages())
			if item.MessageStart < 0 || item.MessageStart >= total {
				_ = sess.Close()
				return errors.New("turn history boundary is invalid")
			}
			_, dropErr := sess.DropTail(total - item.MessageStart)
			return errors.Join(dropErr, sess.Close())
		})
		if err != nil {
			return err
		}
		if err = r.removeOtherGenerationsLocked(item.SessionID, generation); err != nil {
			return err
		}
		if err = r.completeDeletionLocked(item.ID); err != nil {
			return err
		}
	}
	return nil
}

// CopyReader is used only by maintenance tests and tools that need a bounded
// exact copy without teaching online readers to scan alternate sources.
func CopyReader(dst string, src io.Reader, limit int64) error {
	data, err := io.ReadAll(io.LimitReader(src, limit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return errors.New("maintenance input exceeds capacity")
	}
	return storage.Replace(dst, data)
}
