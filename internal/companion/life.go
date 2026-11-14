package companion

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"chat-agent/internal/retrieval"
	"chat-agent/internal/storage"
)

var ErrLifeNotFound = errors.New("life item not found")
var ErrLifeLimit = errors.New("life store limit reached")
var ErrCareBlocked = errors.New("care disabled, quiet, budget exhausted or no longer due")

type Episode struct {
	ID              string     `json:"id"`
	RoleID          string     `json:"role_id"`
	Title           string     `json:"title"`
	Content         string     `json:"content"`
	Status          string     `json:"status"`
	EventAt         *time.Time `json:"event_at"`
	FollowupAt      *time.Time `json:"followup_at"`
	AllowFollowup   bool       `json:"allow_followup"`
	SourceSessionID string     `json:"source_session_id,omitempty"`
	SourceText      string     `json:"source_text,omitempty"`
	Revision        int        `json:"revision"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}
type EpisodeDraft struct {
	Title           string     `json:"title"`
	Content         string     `json:"content"`
	Status          string     `json:"status"`
	EventAt         *time.Time `json:"event_at"`
	FollowupAt      *time.Time `json:"followup_at"`
	AllowFollowup   bool       `json:"allow_followup"`
	SourceSessionID string     `json:"source_session_id,omitempty"`
	SourceText      string     `json:"source_text,omitempty"`
}

// A pointer-to-RawMessage distinguishes an omitted date from an explicit null.
// API code applies patches through EpisodeDraft after decoding date presence.
type CareSettings struct {
	RoleID             string     `json:"role_id"`
	Revision           int        `json:"revision"`
	Enabled            bool       `json:"enabled"`
	AutoGenerate       bool       `json:"auto_generate"`
	AutoProbability    *int       `json:"auto_probability,omitempty"`
	DailyLimit         int        `json:"daily_limit"`
	QuietStart         string     `json:"quiet_start"`
	QuietEnd           string     `json:"quiet_end"`
	DailyTime          string     `json:"daily_time"`
	MinIntervalMinutes int        `json:"min_interval_minutes"`
	SnoozeUntil        *time.Time `json:"snooze_until"`
	UpdatedAt          *time.Time `json:"updated_at,omitempty"`
}
type CareItem struct {
	ID              string     `json:"id"`
	RoleID          string     `json:"role_id"`
	EpisodeID       string     `json:"episode_id,omitempty"`
	Title           string     `json:"title"`
	Status          string     `json:"status"`
	Content         string     `json:"content"`
	Error           string     `json:"error,omitempty"`
	Revision        int        `json:"revision"`
	DueAt           time.Time  `json:"due_at"`
	CreatedAt       time.Time  `json:"created_at"`
	EpisodeRevision int        `json:"episode_revision,omitempty"`
	PolicyRevision  int        `json:"policy_revision,omitempty"`
	AttemptedAt     *time.Time `json:"attempted_at,omitempty"`
	DecisionAt      *time.Time `json:"decision_at,omitempty"`
	Probability     *int       `json:"probability,omitempty"`
	SessionID       string     `json:"session_id,omitempty"`
	MessageIndex    *int       `json:"message_index,omitempty"`
	// Feedback is explicit, role-scoped evidence about this check-in. It is not
	// promoted to long-term memory or treated as a psychological profile.
	Feedback string `json:"feedback,omitempty"`
}
type lifeSnapshot struct {
	Version       int                       `json:"version"`
	Episodes      []Episode                 `json:"episodes"`
	Settings      map[string]CareSettings   `json:"settings"`
	Care          []CareItem                `json:"care"`
	Cuts          map[string]map[string]int `json:"context_cuts"`
	Scheduled     map[string]string         `json:"scheduled_followups"`
	Attempts      map[string]int            `json:"attempts"`
	TopicAttempts map[string]TopicAttempt   `json:"topic_attempts,omitempty"`
}
type LifeStore struct {
	mu        sync.Mutex
	dir, lock string
	state     lifeSnapshot
	closed    bool
	careRoll  func() (int, error)
}

func newLifeSnapshot() lifeSnapshot {
	return lifeSnapshot{Version: 6, Episodes: []Episode{}, Settings: map[string]CareSettings{}, Care: []CareItem{}, Cuts: map[string]map[string]int{}, Scheduled: map[string]string{}, Attempts: map[string]int{}}
}
func OpenLife(dir string) (_ *LifeStore, openErr error) {
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
	s := &LifeStore{dir: dir, lock: lock, state: newLifeSnapshot(), careRoll: randomCareRoll}
	defer func() {
		if openErr != nil {
			s.Close()
		}
	}()
	data, err := os.Open(filepath.Join(dir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(data, (8<<20)+1))
	data.Close()
	if err != nil {
		return nil, err
	}
	if len(b) > 8<<20 {
		return nil, ErrLifeLimit
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	var extra any
	if err = d.Decode(&s.state); err != nil {
		return nil, err
	}
	if d.Decode(&extra) != io.EOF {
		return nil, errors.New("damaged life store")
	}
	if s.state.TopicAttempts == nil {
		s.state.TopicAttempts = map[string]TopicAttempt{}
	}
	// Older v1 snapshots have no interval field. Normalize before validation.
	for role, p := range s.state.Settings {
		if p.MinIntervalMinutes == 0 {
			p.MinIntervalMinutes = 180
		}
		if p.AutoProbability == nil {
			p.AutoProbability = intPointer(50)
		}
		s.state.Settings[role] = p
	}
	if !validLife(s.state) {
		return nil, errors.New("damaged life store")
	}
	next := cloneLife(s.state)
	changed := false
	for i := range next.Care {
		if next.Care[i].Status == "generating" || next.Care[i].Status == "delivering" {
			if next.Care[i].Status == "delivering" {
				next.Care[i].Content = ""
				next.Care[i].SessionID = ""
				next.Care[i].MessageIndex = nil
			}
			next.Care[i].Status = "interrupted"
			next.Care[i].Error = "生成被服务重启中断，不会自动重试"
			next.Care[i].Revision++
			changed = true
		}
	}
	if changed {
		if err = s.save(next); err != nil {
			return nil, err
		}
	}
	return s, nil
}
func cloneLife(value lifeSnapshot) lifeSnapshot {
	b, _ := json.Marshal(value)
	var copy lifeSnapshot
	_ = json.Unmarshal(b, &copy)
	return copy
}
func (s *LifeStore) save(next lifeSnapshot) error {
	if s.closed {
		return errors.New("life store closed")
	}
	// v4 binds delivered proactive messages to a durable conversation index;
	// v5 adds the terminal silent outcome; v6 records explicit check-in feedback.
	next.Version = 6
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if len(b) > 8<<20 {
		return ErrLifeLimit
	}
	if err = storage.Replace(filepath.Join(s.dir, "state.json"), b); err != nil {
		return err
	}
	s.state = next
	return nil
}
func textValid(text string, limit int, multiline bool) bool {
	if !utf8.ValidString(text) || len(text) > limit || strings.TrimSpace(text) == "" {
		return false
	}
	for _, r := range text {
		if unicode.IsControl(r) && !(multiline && (r == '\n' || r == '\t' || r == '\r')) {
			return false
		}
	}
	return true
}
func validEpisode(e Episode) bool {
	return storage.ValidID(e.ID) && storage.ValidID(e.RoleID) && e.Revision > 0 && textValid(e.Title, 256, false) && textValid(e.Content, 4096, true) && (e.Status == "pending" || e.Status == "open" || e.Status == "resolved" || e.Status == "hidden") && (e.SourceSessionID == "" || storage.ValidID(e.SourceSessionID)) && len(e.SourceText) <= 1024 && utf8.ValidString(e.SourceText) && (!e.AllowFollowup || e.FollowupAt != nil)
}
func clockMinute(value string) (int, bool) {
	t, err := time.Parse("15:04", value)
	if err != nil || len(value) != 5 {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}
func validCareSettings(p CareSettings) bool {
	_, start := clockMinute(p.QuietStart)
	_, end := clockMinute(p.QuietEnd)
	_, daily := clockMinute(p.DailyTime)
	return storage.ValidID(p.RoleID) && p.Revision >= 0 && p.DailyLimit >= 1 && p.DailyLimit <= 5 && start && end && (p.DailyTime == "" || daily) && p.MinIntervalMinutes >= 15 && p.MinIntervalMinutes <= 1440 && (p.SnoozeUntil == nil || !p.SnoozeUntil.IsZero()) && (p.AutoProbability == nil || (*p.AutoProbability >= 0 && *p.AutoProbability <= 100))
}
func validLife(state lifeSnapshot) bool {
	if (state.Version < 1 || state.Version > 6) || len(state.Episodes) > 2000 || len(state.Care) > 2000 || len(state.Settings) > 128 || state.Settings == nil || state.Cuts == nil || state.Scheduled == nil || state.Attempts == nil || len(state.TopicAttempts) > 4096 {
		return false
	}
	ids := map[string]bool{}
	for id, attempt := range state.TopicAttempts {
		if !storage.ValidID(id) || !storage.ValidID(attempt.RoleID) || attempt.AttemptedAt.IsZero() {
			return false
		}
	}
	for _, e := range state.Episodes {
		if !validEpisode(e) || ids[e.ID] {
			return false
		}
		ids[e.ID] = true
	}
	ids = map[string]bool{}
	for _, c := range state.Care {
		if !storage.ValidID(c.ID) || !storage.ValidID(c.RoleID) || c.Revision < 1 || len(c.Content) > 8192 || len(c.Title) > 256 || ids[c.ID] {
			return false
		}
		if (c.Probability != nil && (*c.Probability < 0 || *c.Probability > 100)) || (c.Status == "skipped" && (c.DecisionAt == nil || c.AttemptedAt != nil || c.Probability == nil)) {
			return false
		}
		if c.Feedback != "" && c.Feedback != "helpful" && c.Feedback != "less" {
			return false
		}
		if (c.SessionID == "") != (c.MessageIndex == nil) || c.SessionID != "" && (!storage.ValidID(c.SessionID) || *c.MessageIndex < 0 || c.Status != "delivering" && c.Status != "ready" && c.Status != "seen") {
			return false
		}
		ids[c.ID] = true
		switch c.Status {
		case "due", "generating", "delivering", "ready", "seen", "canceled", "failed", "interrupted", "skipped", "silent":
		default:
			return false
		}
	}
	for role, p := range state.Settings {
		if role != p.RoleID || !validCareSettings(p) {
			return false
		}
	}
	cutCount := 0
	for role, cuts := range state.Cuts {
		if !storage.ValidID(role) {
			return false
		}
		for id, n := range cuts {
			cutCount++
			if !storage.ValidID(id) || n < 0 {
				return false
			}
		}
	}
	return cutCount <= 8192 && len(state.Attempts) <= 4096 && len(state.Scheduled) <= 2000
}
func lifeID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func stableCareID(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}
func defaultCare(role string) CareSettings {
	return CareSettings{RoleID: role, DailyLimit: 1, AutoProbability: intPointer(50), QuietStart: "22:00", QuietEnd: "08:00", MinIntervalMinutes: 180}
}
func intPointer(value int) *int { return &value }
func randomCareRoll() (int, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(100))
	if err != nil {
		return 0, err
	}
	return int(n.Int64()), nil
}

func copyCareSettings(p CareSettings) CareSettings {
	if p.AutoProbability != nil {
		p.AutoProbability = intPointer(*p.AutoProbability)
	}
	if p.SnoozeUntil != nil {
		value := *p.SnoozeUntil
		p.SnoozeUntil = &value
	}
	if p.UpdatedAt != nil {
		value := *p.UpdatedAt
		p.UpdatedAt = &value
	}
	return p
}
func (s *LifeStore) ready(role string) error {
	if s.closed {
		return errors.New("life store closed")
	}
	if !storage.ValidID(role) {
		return ErrInvalid
	}
	return nil
}
func (s *LifeStore) Episodes(role string) ([]Episode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return nil, err
	}
	out := []Episode{}
	for _, e := range cloneLife(s.state).Episodes {
		if e.RoleID == role {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}
func (s *LifeStore) Episode(role, id string) (Episode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return Episode{}, err
	}
	for _, e := range cloneLife(s.state).Episodes {
		if e.ID == id && e.RoleID == role {
			return e, nil
		}
	}
	return Episode{}, ErrLifeNotFound
}
func (s *LifeStore) AddEpisode(role string, draft EpisodeDraft) (Episode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return Episode{}, err
	}
	if draft.Status == "" {
		draft.Status = "open"
	}
	id, err := lifeID()
	if err != nil {
		return Episode{}, err
	}
	now := time.Now().UTC()
	e := Episode{ID: id, RoleID: role, Title: strings.TrimSpace(draft.Title), Content: strings.TrimSpace(draft.Content), Status: draft.Status, EventAt: draft.EventAt, FollowupAt: draft.FollowupAt, AllowFollowup: draft.AllowFollowup, SourceSessionID: draft.SourceSessionID, SourceText: draft.SourceText, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if !validEpisode(e) {
		return Episode{}, ErrInvalid
	}
	count := 0
	for _, old := range s.state.Episodes {
		if old.RoleID != role {
			continue
		}
		count++
		if old.Title == e.Title && old.Content == e.Content && old.Status == e.Status && old.SourceSessionID == e.SourceSessionID && old.SourceText == e.SourceText && sameTime(old.EventAt, e.EventAt) && sameTime(old.FollowupAt, e.FollowupAt) && old.AllowFollowup == e.AllowFollowup {
			return old, nil
		}
	}
	if count >= 500 || len(s.state.Episodes) >= 2000 {
		return Episode{}, ErrLifeLimit
	}
	next := cloneLife(s.state)
	next.Episodes = append(next.Episodes, e)
	if err = s.save(next); err != nil {
		return Episode{}, err
	}
	return e, nil
}
func sameTime(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
}
func applyCuts(next *lifeSnapshot, role string, cuts map[string]int) error {
	if len(cuts) == 0 {
		return nil
	}
	if next.Cuts[role] == nil {
		next.Cuts[role] = map[string]int{}
	}
	for id, n := range cuts {
		if !storage.ValidID(id) || n < 0 {
			return ErrInvalid
		}
		if n > next.Cuts[role][id] {
			next.Cuts[role][id] = n
		}
	}
	count := 0
	for _, values := range next.Cuts {
		count += len(values)
	}
	if count > 8192 {
		return ErrLifeLimit
	}
	return nil
}
func cancelEpisodeCare(next *lifeSnapshot, id string) {
	for i := range next.Care {
		item := &next.Care[i]
		if item.EpisodeID == id && (item.Status == "due" || item.Status == "generating") {
			item.Status = "canceled"
			item.Revision++
		}
	}
}
func (s *LifeStore) ReplaceEpisode(role, id string, revision int, draft EpisodeDraft, cuts map[string]int) (Episode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return Episode{}, err
	}
	next := cloneLife(s.state)
	for i, old := range next.Episodes {
		if old.RoleID != role || old.ID != id {
			continue
		}
		if old.Revision != revision {
			return Episode{}, ErrConflict
		}
		e := old
		e.Title = strings.TrimSpace(draft.Title)
		e.Content = strings.TrimSpace(draft.Content)
		e.Status = draft.Status
		e.EventAt = draft.EventAt
		e.FollowupAt = draft.FollowupAt
		e.AllowFollowup = draft.AllowFollowup
		e.Revision++
		e.UpdatedAt = time.Now().UTC()
		if !validEpisode(e) {
			return Episode{}, ErrInvalid
		}
		next.Episodes[i] = e
		if err := applyCuts(&next, role, cuts); err != nil {
			return Episode{}, err
		}
		cancelEpisodeCare(&next, id)
		if err := s.save(next); err != nil {
			return Episode{}, err
		}
		return e, nil
	}
	return Episode{}, ErrLifeNotFound
}
func (s *LifeStore) DeleteEpisode(role, id string, revision int, cuts map[string]int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return err
	}
	next := cloneLife(s.state)
	for i, e := range next.Episodes {
		if e.RoleID != role || e.ID != id {
			continue
		}
		if e.Revision != revision {
			return ErrConflict
		}
		next.Episodes = append(next.Episodes[:i], next.Episodes[i+1:]...)
		if err := applyCuts(&next, role, cuts); err != nil {
			return err
		}
		cancelEpisodeCare(&next, id)
		delete(next.Scheduled, id)
		return s.save(next)
	}
	return ErrLifeNotFound
}
func (s *LifeStore) Floor(role, sessionID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return 0, err
	}
	if !storage.ValidID(sessionID) {
		return 0, ErrInvalid
	}
	return s.state.Cuts[role][sessionID], nil
}

// AdvanceCuts is a conservative write barrier for corrections in other stores.
// Publishing it before a memory mutation prevents old transcripts resurfacing.
func (s *LifeStore) AdvanceCuts(role string, cuts map[string]int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return err
	}
	if len(cuts) == 0 {
		return nil
	}
	next := cloneLife(s.state)
	if err := applyCuts(&next, role, cuts); err != nil {
		return err
	}
	return s.save(next)
}
func (s *LifeStore) ResetFloor(role, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return err
	}
	if !storage.ValidID(sessionID) {
		return ErrInvalid
	}
	if s.state.Cuts[role][sessionID] == 0 {
		return nil
	}
	next := cloneLife(s.state)
	delete(next.Cuts[role], sessionID)
	return s.save(next)
}
func (s *LifeStore) Context(role, query string) ([]Episode, error) {
	entries, err := s.Episodes(role)
	if err != nil {
		return nil, err
	}
	// Context is automatic reference selection, not an explicit recent-items list.
	if strings.TrimSpace(query) == "" {
		return []Episode{}, nil
	}
	selected := []Episode{}
	documents := []retrieval.Document{}
	for _, e := range entries {
		if e.Status != "open" && e.Status != "resolved" {
			continue
		}
		selected = append(selected, e)
		documents = append(documents, retrieval.Document{ID: e.ID, Text: e.Title + " " + e.Content, UpdatedAt: e.UpdatedAt})
	}
	matches := retrieval.Rank(query, documents, len(documents))
	now := time.Now()
	near := func(e Episode) int {
		if e.EventAt == nil {
			return 0
		}
		distance := now.Sub(*e.EventAt)
		if distance < 0 {
			distance = -distance
		}
		if distance <= 7*24*time.Hour {
			return 2
		}
		if distance <= 30*24*time.Hour {
			return 1
		}
		return 0
	}
	// Event time only breaks lexical ties; it cannot admit unrelated events.
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return near(selected[matches[i].Index]) > near(selected[matches[j].Index])
	})
	out := []Episode{}
	for _, m := range matches[:min(5, len(matches))] {
		entry := selected[m.Index]
		// Original evidence can contain a superseded fact after correction.
		entry.SourceText = ""
		out = append(out, entry)
	}
	return out, nil
}
func (s *LifeStore) Settings(role string) (CareSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return CareSettings{}, err
	}
	p, ok := s.state.Settings[role]
	if !ok {
		p = defaultCare(role)
	}
	return copyCareSettings(p), nil
}
func (s *LifeStore) SetSettings(role string, p CareSettings) (CareSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return CareSettings{}, err
	}
	p = copyCareSettings(p)
	old, ok := s.state.Settings[role]
	if !ok {
		old = defaultCare(role)
	}
	if old.Revision != p.Revision {
		return CareSettings{}, ErrConflict
	}
	p.RoleID = role
	if p.AutoProbability == nil {
		p.AutoProbability = copyCareSettings(old).AutoProbability
	}
	if p.AutoProbability == nil {
		p.AutoProbability = intPointer(50)
	}
	if p.MinIntervalMinutes == 0 {
		p.MinIntervalMinutes = old.MinIntervalMinutes
	}
	if !validCareSettings(p) {
		return CareSettings{}, ErrInvalid
	}
	if !ok && len(s.state.Settings) >= 128 {
		return CareSettings{}, ErrLifeLimit
	}
	p.Revision++
	now := time.Now().UTC()
	p.UpdatedAt = &now
	next := cloneLife(s.state)
	next.Settings[role] = p
	if !p.Enabled {
		for i := range next.Care {
			c := &next.Care[i]
			if c.RoleID == role && (c.Status == "due" || c.Status == "generating") {
				c.Status = "canceled"
				c.Revision++
			}
		}
	}
	if err := s.save(next); err != nil {
		return CareSettings{}, err
	}
	return copyCareSettings(p), nil
}
func quiet(p CareSettings, now time.Time, location *time.Location) bool {
	start, _ := clockMinute(p.QuietStart)
	end, _ := clockMinute(p.QuietEnd)
	if start == end {
		return false
	}
	local := now.In(location)
	minute := local.Hour()*60 + local.Minute()
	if start < end {
		return minute >= start && minute < end
	}
	return minute >= start || minute < end
}
func (s *LifeStore) Tick(role string, now time.Time, location *time.Location) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return err
	}
	p, ok := s.state.Settings[role]
	if !ok || !p.Enabled {
		return nil
	}
	next := cloneLife(s.state)
	changed := false
	add := func(id, episodeID, title string, due time.Time, episodeRevision int) {
		for _, c := range next.Care {
			if c.ID == id {
				return
			}
		}
		next.Care = append(next.Care, CareItem{ID: id, RoleID: role, EpisodeID: episodeID, Title: title, Status: "due", Revision: 1, DueAt: due, CreatedAt: now.UTC(), EpisodeRevision: episodeRevision})
		changed = true
	}
	for _, e := range next.Episodes {
		if e.RoleID != role || e.Status != "open" || !e.AllowFollowup || e.FollowupAt == nil || e.FollowupAt.After(now) {
			continue
		}
		key := e.FollowupAt.UTC().Format(time.RFC3339Nano)
		if next.Scheduled[e.ID] == key {
			continue
		}
		id := stableCareID("episode:" + e.ID + ":" + key)
		add(id, e.ID, e.Title, *e.FollowupAt, e.Revision)
		next.Scheduled[e.ID] = key
		changed = true
	}
	if p.DailyTime != "" {
		local := now.In(location)
		minute, _ := clockMinute(p.DailyTime)
		due := time.Date(local.Year(), local.Month(), local.Day(), minute/60, minute%60, 0, 0, location)
		if !now.Before(due) {
			id := stableCareID("daily:" + role + ":" + local.Format("2006-01-02"))
			add(id, "", "今天想和你聊一会儿", due, 0)
		}
	}
	if !changed {
		return nil
	}
	// Only trim old, terminal inbox entries; current-day dedup records remain.
	filtered := next.Care[:0]
	for _, c := range next.Care {
		terminal := c.Status == "ready" || c.Status == "seen" || c.Status == "canceled" || c.Status == "failed" || c.Status == "interrupted" || c.Status == "skipped" || c.Status == "silent"
		if terminal && c.CreatedAt.Before(now.AddDate(0, 0, -30)) {
			continue
		}
		filtered = append(filtered, c)
	}
	next.Care = filtered
	if len(next.Care) > 2000 {
		return ErrLifeLimit
	}
	return s.save(next)
}
func (s *LifeStore) Care(role string) ([]CareItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return nil, err
	}
	out := []CareItem{}
	for _, c := range cloneLife(s.state).Care {
		if c.RoleID == role {
			if c.Status == "delivering" {
				c.Content = ""
				c.SessionID = ""
				c.MessageIndex = nil
			}
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}
func (s *LifeStore) ClaimCare(role, id string, automatic bool, now time.Time, location *time.Location) (CareItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return CareItem{}, err
	}
	p, ok := s.state.Settings[role]
	if !ok || !p.Enabled || (automatic && !p.AutoGenerate) || quiet(p, now, location) || (p.SnoozeUntil != nil && now.Before(*p.SnoozeUntil)) {
		return CareItem{}, ErrCareBlocked
	}
	for _, old := range s.state.Care {
		if old.RoleID != role {
			continue
		}
		attempt := old.AttemptedAt
		if attempt == nil && (old.Status == "generating" || old.Status == "ready" || old.Status == "seen" || old.Status == "failed" || old.Status == "interrupted") {
			timestamp := old.CreatedAt
			attempt = &timestamp
		}
		if attempt != nil && now.Before(attempt.Add(time.Duration(p.MinIntervalMinutes)*time.Minute)) {
			return CareItem{}, ErrCareBlocked
		}
	}
	for _, attempt := range s.state.TopicAttempts {
		if attempt.RoleID == role && now.Before(attempt.AttemptedAt.Add(time.Duration(p.MinIntervalMinutes)*time.Minute)) {
			return CareItem{}, ErrCareBlocked
		}
	}
	key := role + "|" + now.In(location).Format("2006-01-02")
	if s.state.Attempts[key] >= p.DailyLimit {
		return CareItem{}, ErrCareBlocked
	}
	next := cloneLife(s.state)
	for i, c := range next.Care {
		if c.RoleID != role || c.ID != id {
			continue
		}
		if c.Status != "due" || c.DueAt.After(now) {
			return CareItem{}, ErrCareBlocked
		}
		if c.EpisodeID != "" {
			valid := false
			for _, e := range next.Episodes {
				if e.ID == c.EpisodeID && e.RoleID == role && e.Status == "open" && e.AllowFollowup && e.Revision == c.EpisodeRevision {
					valid = true
				}
			}
			if !valid {
				return CareItem{}, ErrCareBlocked
			}
		}
		// Only optional automatic daily greetings are sampled. Explicit event
		// followups and manual generation keep their existing semantics.
		if automatic && c.EpisodeID == "" && c.DecisionAt == nil {
			probability := 50
			if p.AutoProbability != nil {
				probability = *p.AutoProbability
			}
			draw := 0
			if probability > 0 && probability < 100 {
				var err error
				draw, err = s.careRoll()
				if err != nil {
					return CareItem{}, err
				}
				if draw < 0 || draw >= 100 {
					return CareItem{}, errors.New("invalid care random sample")
				}
			}
			decisionAt := now.UTC()
			c.DecisionAt = &decisionAt
			c.Probability = intPointer(probability)
			c.PolicyRevision = p.Revision
			if probability == 0 || (probability < 100 && draw >= probability) {
				c.Status = "skipped"
				c.Revision++
				next.Care[i] = c
				if err := s.save(next); err != nil {
					return CareItem{}, err
				}
				return c, nil
			}
		}
		c.Status = "generating"
		c.Revision++
		c.PolicyRevision = p.Revision
		attemptedAt := now.UTC()
		c.AttemptedAt = &attemptedAt
		next.Care[i] = c
		next.Attempts[key]++
		cutoff := now.In(location).AddDate(0, 0, -30).Format("2006-01-02")
		for k := range next.Attempts {
			parts := strings.Split(k, "|")
			if len(parts) == 2 && parts[1] < cutoff {
				delete(next.Attempts, k)
			}
		}
		if err := s.save(next); err != nil {
			return CareItem{}, err
		}
		return c, nil
	}
	return CareItem{}, ErrLifeNotFound
}
func (s *LifeStore) FinishCare(role, id string, revision int, text, safeError string) (CareItem, error) {
	return s.FinishCareInSession(role, id, revision, text, safeError, "", -1)
}

// SilenceCare settles a paid proactive attempt without publishing a message.
// Silence is a successful host decision, not a failure and not a retry signal.
func (s *LifeStore) SilenceCare(role, id string, revision int) (CareItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return CareItem{}, err
	}
	next := cloneLife(s.state)
	for i, c := range next.Care {
		if c.RoleID != role || c.ID != id {
			continue
		}
		if c.Status != "generating" || c.Revision != revision {
			return CareItem{}, ErrConflict
		}
		c.Status = "silent"
		c.Content = ""
		c.Error = ""
		c.SessionID = ""
		c.MessageIndex = nil
		c.Revision++
		next.Care[i] = c
		if err := s.save(next); err != nil {
			return CareItem{}, err
		}
		return c, nil
	}
	return CareItem{}, ErrLifeNotFound
}

func (s *LifeStore) FinishCareInSession(role, id string, revision int, text, safeError, sessionID string, messageIndex int) (CareItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if (sessionID == "") != (messageIndex < 0) || sessionID != "" && (!storage.ValidID(sessionID) || messageIndex < 0) {
		return CareItem{}, ErrInvalid
	}
	if err := s.ready(role); err != nil {
		return CareItem{}, err
	}
	next := cloneLife(s.state)
	for i, c := range next.Care {
		if c.RoleID != role || c.ID != id {
			continue
		}
		if c.Status != "generating" || c.Revision != revision {
			return CareItem{}, ErrConflict
		}
		p, ok := next.Settings[role]
		valid := ok && p.Enabled && p.Revision == c.PolicyRevision
		if c.EpisodeID != "" {
			found := false
			for _, e := range next.Episodes {
				if e.ID == c.EpisodeID && e.RoleID == role && e.Status == "open" && e.AllowFollowup && e.Revision == c.EpisodeRevision {
					found = true
				}
			}
			valid = valid && found
		}
		c.Revision++
		if !valid {
			c.Status = "canceled"
			c.Content = ""
		} else if safeError != "" {
			c.Status = "failed"
			c.Error = safeError
			c.Content = ""
		} else if !textValid(text, 8192, true) {
			return CareItem{}, ErrInvalid
		} else {
			c.Status = "ready"
			c.Content = strings.TrimSpace(text)
			if sessionID != "" {
				c.SessionID = sessionID
				c.MessageIndex = intPointer(messageIndex)
			}
		}
		next.Care[i] = c
		if err := s.save(next); err != nil {
			return CareItem{}, err
		}
		return c, nil
	}
	return CareItem{}, ErrLifeNotFound
}

// PrepareCareDelivery validates the final policy and records a non-visible
// delivery intent. The caller must durably append the assistant message before
// CommitCareDelivery makes the item eligible for inbox and push delivery.
func (s *LifeStore) PrepareCareDelivery(role, id string, revision int, text, sessionID string, messageIndex int) (CareItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !storage.ValidID(sessionID) || messageIndex < 0 || !textValid(text, 8192, true) {
		return CareItem{}, ErrInvalid
	}
	if err := s.ready(role); err != nil {
		return CareItem{}, err
	}
	next := cloneLife(s.state)
	for i, c := range next.Care {
		if c.RoleID != role || c.ID != id {
			continue
		}
		if c.Status != "generating" || c.Revision != revision {
			return CareItem{}, ErrConflict
		}
		p, ok := next.Settings[role]
		valid := ok && p.Enabled && p.Revision == c.PolicyRevision
		if c.EpisodeID != "" {
			found := false
			for _, e := range next.Episodes {
				if e.ID == c.EpisodeID && e.RoleID == role && e.Status == "open" && e.AllowFollowup && e.Revision == c.EpisodeRevision {
					found = true
				}
			}
			valid = valid && found
		}
		c.Revision++
		if !valid {
			c.Status = "canceled"
			c.Content = ""
		} else {
			c.Status = "delivering"
			c.Content = strings.TrimSpace(text)
			c.SessionID = sessionID
			c.MessageIndex = intPointer(messageIndex)
		}
		next.Care[i] = c
		if err := s.save(next); err != nil {
			return CareItem{}, err
		}
		return c, nil
	}
	return CareItem{}, ErrLifeNotFound
}

func (s *LifeStore) CommitCareDelivery(role, id string, revision int) (CareItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return CareItem{}, err
	}
	next := cloneLife(s.state)
	for i, c := range next.Care {
		if c.RoleID != role || c.ID != id {
			continue
		}
		if c.Status != "delivering" || c.Revision != revision {
			return CareItem{}, ErrConflict
		}
		c.Status = "ready"
		c.Revision++
		next.Care[i] = c
		if err := s.save(next); err != nil {
			return CareItem{}, err
		}
		return c, nil
	}
	return CareItem{}, ErrLifeNotFound
}

// FailCareDelivery clears a prepared or ready record when its assistant
// message could not be committed. The paid attempt remains charged and is
// never retried automatically.
func (s *LifeStore) FailCareDelivery(role, id string, revision int, safeError string) (CareItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return CareItem{}, err
	}
	next := cloneLife(s.state)
	for i, c := range next.Care {
		if c.RoleID != role || c.ID != id {
			continue
		}
		if c.Status != "delivering" && c.Status != "ready" || c.Revision != revision || strings.TrimSpace(safeError) == "" {
			return CareItem{}, ErrConflict
		}
		c.Status = "failed"
		c.Error = safeError
		c.Content = ""
		c.SessionID = ""
		c.MessageIndex = nil
		c.Revision++
		next.Care[i] = c
		if err := s.save(next); err != nil {
			return CareItem{}, err
		}
		return c, nil
	}
	return CareItem{}, ErrLifeNotFound
}
func (s *LifeStore) UpdateCare(role, id string, revision int, status string) (CareItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return CareItem{}, err
	}
	if status != "seen" && status != "canceled" {
		return CareItem{}, ErrInvalid
	}
	next := cloneLife(s.state)
	for i, c := range next.Care {
		if c.RoleID != role || c.ID != id {
			continue
		}
		if c.Revision != revision {
			return CareItem{}, ErrConflict
		}
		if c.Status == "delivering" {
			return CareItem{}, ErrConflict
		}
		if status == "seen" && c.Status != "ready" && c.Status != "seen" {
			return CareItem{}, ErrInvalid
		}
		if c.Status == status {
			return c, nil
		}
		c.Status = status
		c.Revision++
		next.Care[i] = c
		if err := s.save(next); err != nil {
			return CareItem{}, err
		}
		return c, nil
	}
	return CareItem{}, ErrLifeNotFound
}

// SetCareFeedback records an explicit reaction to a delivered check-in. The
// revision guard prevents an old browser tab from overwriting newer feedback.
func (s *LifeStore) SetCareFeedback(role, id string, revision int, feedback string) (CareItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return CareItem{}, err
	}
	if feedback != "helpful" && feedback != "less" {
		return CareItem{}, ErrInvalid
	}
	next := cloneLife(s.state)
	for i, c := range next.Care {
		if c.RoleID != role || c.ID != id {
			continue
		}
		if c.Revision != revision {
			return CareItem{}, ErrConflict
		}
		if (c.Status != "ready" && c.Status != "seen") || c.Content == "" {
			return CareItem{}, ErrInvalid
		}
		if c.Feedback == feedback {
			return c, nil
		}
		c.Feedback = feedback
		c.Revision++
		next.Care[i] = c
		if err := s.save(next); err != nil {
			return CareItem{}, err
		}
		return c, nil
	}
	return CareItem{}, ErrLifeNotFound
}
func (s *LifeStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return os.Remove(s.lock)
}
