// Package topics keeps bounded public-discovery opportunities and their journals.
// A claimed opportunity is charged durably and is never retried after restart.
package topics

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"pocket-agent/internal/storage"
)

var (
	ErrInvalid  = errors.New("invalid topic request")
	ErrConflict = errors.New("topic or settings changed; reload before saving")
	ErrNotFound = errors.New("topic opportunity not found")
	ErrBlocked  = errors.New("topic discovery blocked by policy or budget")
	ErrLimit    = errors.New("topic store capacity reached")
)

type Settings struct {
	RoleID          string   `json:"role_id"`
	Revision        int      `json:"revision"`
	Enabled         bool     `json:"enabled"`
	Queries         []string `json:"queries"`
	WindowStart     string   `json:"window_start"`
	WindowEnd       string   `json:"window_end"`
	VisitsPerDay    int      `json:"visits_per_day"`
	ScanProbability int      `json:"scan_probability"`
	DailyLimit      int      `json:"daily_limit"`
}

type Source struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

type Record struct {
	ID             string    `json:"id"`
	RoleID         string    `json:"role_id"`
	Revision       int       `json:"revision"`
	PolicyRevision int       `json:"policy_revision"`
	Status         string    `json:"status"`
	Reason         string    `json:"reason,omitempty"`
	DueAt          time.Time `json:"due_at"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	Query          string    `json:"query,omitempty"`
	Sources        []Source  `json:"sources,omitempty"`
	Content        string    `json:"content,omitempty"`
	SearchCount    int       `json:"search_count"`
	ReadCount      int       `json:"read_count"`
	ModelCalls     int       `json:"model_calls,omitempty"`
	SessionID      string    `json:"session_id,omitempty"`
	MessageIndex   *int      `json:"message_index,omitempty"`
}

type Result struct {
	Status       string   `json:"status"`
	Reason       string   `json:"reason,omitempty"`
	Content      string   `json:"content,omitempty"`
	Sources      []Source `json:"sources,omitempty"`
	SearchCount  int      `json:"search_count"`
	ReadCount    int      `json:"read_count"`
	ModelCalls   int      `json:"model_calls,omitempty"`
	SessionID    string   `json:"session_id,omitempty"`
	MessageIndex *int     `json:"message_index,omitempty"`
}

// Day entries survive record deletion: neither opportunity deduplication nor
// daily charges may depend on the visible journals.
type day struct {
	Date   string `json:"date"`
	RoleID string `json:"role_id"`
	Claims int    `json:"claims"`
}
type snapshot struct {
	Version   int        `json:"version"`
	Settings  []Settings `json:"settings"`
	Records   []Record   `json:"records"`
	Days      []day      `json:"days"`
	LastClaim time.Time  `json:"last_claim,omitempty"`
}
type Store struct {
	mu        sync.Mutex
	dir, lock string
	closed    bool
	state     snapshot
	now       func() time.Time
	random    func(int) (int, error)
}

func defaults(role string) Settings {
	return Settings{RoleID: role, WindowStart: "09:00", WindowEnd: "21:00", VisitsPerDay: 2, ScanProbability: 70, DailyLimit: 3, Queries: []string{}}
}
func minute(v string) (int, bool) {
	t, err := time.Parse("15:04", v)
	return t.Hour()*60 + t.Minute(), err == nil && t.Format("15:04") == v
}
func textOK(v string, limit int, empty bool) bool {
	if !utf8.ValidString(v) || utf8.RuneCountInString(v) > limit || strings.TrimSpace(v) != v || !empty && v == "" {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}
func validSettings(p Settings) bool {
	a, okA := minute(p.WindowStart)
	b, okB := minute(p.WindowEnd)
	if !storage.ValidID(p.RoleID) || p.Revision < 0 || !okA || !okB || a >= b || p.VisitsPerDay < 1 || p.VisitsPerDay > 2 || p.ScanProbability < 0 || p.ScanProbability > 100 || p.DailyLimit < 1 || p.DailyLimit > 3 || len(p.Queries) > 5 || p.Enabled && len(p.Queries) == 0 {
		return false
	}
	for _, q := range p.Queries {
		if !textOK(q, 80, false) {
			return false
		}
	}
	return true
}
func validStatus(status string) bool {
	switch status {
	case "scheduled", "skipped", "running", "delivering", "ready", "silent", "failed", "interrupted", "expired", "canceled", "seen":
		return true
	}
	return false
}
func validSource(src Source) bool {
	u, err := url.Parse(src.URL)
	if err != nil || len(src.URL) > 2048 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || !textOK(src.Title, 300, true) {
		return false
	}
	h := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") || !strings.Contains(h, ".") {
		return false
	}
	if ip := net.ParseIP(h); ip != nil {
		if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			return false
		}
		addr, ok := netip.AddrFromSlice(ip)
		if !ok {
			return false
		}
		addr = addr.Unmap()
		for _, p := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001::/23", "2001:db8::/32", "2002::/16"} {
			if netip.MustParsePrefix(p).Contains(addr) {
				return false
			}
		}
	}
	return true
}
func validResult(r Result) bool {
	if r.Status != "ready" && r.Status != "silent" && r.Status != "failed" && r.Status != "interrupted" {
		return false
	}
	if len(r.Content) > 16000 || !utf8.ValidString(r.Content) || !textOK(r.Reason, 500, true) || r.SearchCount < 0 || r.SearchCount > 1 || r.ReadCount < 0 || r.ReadCount > 2 || r.ModelCalls < 0 || r.ModelCalls > 1 || len(r.Sources) > 2 || r.Status == "ready" && strings.TrimSpace(r.Content) == "" {
		return false
	}
	if (r.SessionID == "") != (r.MessageIndex == nil) || r.SessionID != "" && (!storage.ValidID(r.SessionID) || *r.MessageIndex < 0 || r.Status != "ready") {
		return false
	}
	for _, src := range r.Sources {
		if !validSource(src) {
			return false
		}
	}
	return true
}

func Open(dir string) (_ *Store, err error) {
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock := filepath.Join(dir, ".store.lock")
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("topics store lock unavailable: %w", err)
	}
	_, err = fmt.Fprintf(f, "pid=%d\n", os.Getpid())
	err = errors.Join(err, f.Close())
	if err != nil {
		_ = os.Remove(lock)
		return nil, err
	}
	s := &Store{dir: dir, lock: lock, state: snapshot{Version: 2}, now: time.Now, random: func(n int) (int, error) {
		v, e := rand.Int(rand.Reader, big.NewInt(int64(n)))
		if e != nil {
			return 0, e
		}
		return int(v.Int64()), nil
	}}
	defer func() {
		if err != nil {
			_ = s.Close()
		}
	}()
	input, err := os.Open(filepath.Join(dir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(input, (24<<20)+1))
	closeErr := input.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(data) > 24<<20 {
		return nil, ErrLimit
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var extra any
	if err = d.Decode(&s.state); err != nil {
		return nil, err
	}
	if d.Decode(&extra) != io.EOF || (s.state.Version != 1 && s.state.Version != 2) || len(s.state.Records) > 1024 || len(s.state.Settings) > 128 || len(s.state.Days) > 8192 {
		return nil, ErrInvalid
	}
	roles := map[string]bool{}
	ids := map[string]bool{}
	days := map[string]bool{}
	for _, p := range s.state.Settings {
		if !validSettings(p) || roles[p.RoleID] || p.Revision < 1 {
			return nil, ErrInvalid
		}
		roles[p.RoleID] = true
	}
	for _, r := range s.state.Records {
		if !storage.ValidID(r.ID) || !roles[r.RoleID] || ids[r.ID] || r.Revision < 1 || r.PolicyRevision < 1 || !validStatus(r.Status) || r.DueAt.IsZero() || r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() || len(r.Content) > 16000 || !textOK(r.Query, 80, true) || !textOK(r.Reason, 500, true) || r.SearchCount < 0 || r.SearchCount > 1 || r.ReadCount < 0 || r.ReadCount > 2 || r.ModelCalls < 0 || r.ModelCalls > 1 || len(r.Sources) > 2 || (r.SessionID == "") != (r.MessageIndex == nil) || r.SessionID != "" && (!storage.ValidID(r.SessionID) || *r.MessageIndex < 0 || r.Status != "delivering" && r.Status != "ready" && r.Status != "seen") {
			return nil, ErrInvalid
		}
		for _, src := range r.Sources {
			if !validSource(src) {
				return nil, ErrInvalid
			}
		}
		ids[r.ID] = true
	}
	charges := map[string]int{}
	for _, d := range s.state.Days {
		_, e := time.Parse("2006-01-02", d.Date)
		key := d.Date + "/" + d.RoleID
		charges[d.Date] += d.Claims
		if e != nil || !roles[d.RoleID] || days[key] || d.Claims < 0 || d.Claims > 3 || charges[d.Date] > 3 {
			return nil, ErrInvalid
		}
		days[key] = true
	}
	changed := false
	for i := range s.state.Records {
		r := &s.state.Records[i]
		if r.Status == "running" || r.Status == "delivering" {
			if r.Status == "delivering" {
				r.Content = ""
				r.Sources = nil
				r.SessionID = ""
				r.MessageIndex = nil
			}
			change(r, "interrupted", "process restarted; opportunity will not replay", s.now())
			changed = true
		}
	}
	if changed {
		if err = s.commit(s.state); err != nil {
			return nil, err
		}
	}
	return s, nil
}
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return os.Remove(s.lock)
}
func (s *Store) check(role string) error {
	if s.closed {
		return errors.New("topics store closed")
	}
	if !storage.ValidID(role) {
		return ErrInvalid
	}
	return nil
}
func (s *Store) clone() snapshot {
	n := s.state
	n.Settings = append([]Settings(nil), n.Settings...)
	n.Records = append([]Record(nil), n.Records...)
	n.Days = append([]day(nil), n.Days...)
	for i := range n.Settings {
		n.Settings[i].Queries = append([]string(nil), n.Settings[i].Queries...)
	}
	for i := range n.Records {
		n.Records[i].Sources = append([]Source(nil), n.Records[i].Sources...)
		if n.Records[i].MessageIndex != nil {
			index := *n.Records[i].MessageIndex
			n.Records[i].MessageIndex = &index
		}
	}
	return n
}
func (s *Store) commit(n snapshot) error {
	n.Version = 2
	data, err := json.MarshalIndent(n, "", "  ")
	if err != nil {
		return err
	}
	if err = storage.Replace(filepath.Join(s.dir, "state.json"), data); err != nil {
		return err
	}
	s.state = n
	return nil
}
func setting(n snapshot, role string) Settings {
	for _, p := range n.Settings {
		if p.RoleID == role {
			p.Queries = append([]string(nil), p.Queries...)
			return p
		}
	}
	return defaults(role)
}
func (s *Store) Settings(role string) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(role); err != nil {
		return Settings{}, err
	}
	return setting(s.state, role), nil
}
func (s *Store) AllSettings() ([]Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("topics store closed")
	}
	return s.clone().Settings, nil
}
func change(r *Record, status, reason string, now time.Time) {
	r.Status = status
	r.Reason = reason
	r.UpdatedAt = now
	r.Revision++
}
func copyRecord(r Record) Record {
	r.Sources = append([]Source(nil), r.Sources...)
	if r.MessageIndex != nil {
		index := *r.MessageIndex
		r.MessageIndex = &index
	}
	return r
}
func (s *Store) UpdateSettings(role string, p Settings) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(role); err != nil {
		return Settings{}, err
	}
	old := setting(s.state, role)
	if p.Revision != old.Revision {
		return Settings{}, ErrConflict
	}
	if p.RoleID != "" && p.RoleID != role {
		return Settings{}, ErrInvalid
	}
	p.RoleID = role
	if p.WindowStart == "" {
		p.WindowStart = old.WindowStart
	}
	if p.WindowEnd == "" {
		p.WindowEnd = old.WindowEnd
	}
	if p.VisitsPerDay == 0 {
		p.VisitsPerDay = old.VisitsPerDay
	}
	if p.DailyLimit == 0 {
		p.DailyLimit = old.DailyLimit
	}
	if !validSettings(p) {
		return Settings{}, ErrInvalid
	}
	p.Revision++
	p.Queries = append([]string(nil), p.Queries...)
	n := s.clone()
	found := false
	for i := range n.Settings {
		if n.Settings[i].RoleID == role {
			n.Settings[i] = p
			found = true
		}
	}
	if !found {
		if len(n.Settings) >= 128 {
			return Settings{}, ErrLimit
		}
		n.Settings = append(n.Settings, p)
	}
	for i := range n.Records {
		r := &n.Records[i]
		if r.RoleID == role && (r.Status == "scheduled" || r.Status == "running") {
			change(r, "canceled", "discovery settings changed", s.now())
		}
	}
	if err := s.commit(n); err != nil {
		return Settings{}, err
	}
	return setting(s.state, role), nil
}
func trim(n *snapshot, now time.Time) {
	cut := now.AddDate(0, 0, -30)
	records := n.Records[:0]
	for _, r := range n.Records {
		if r.UpdatedAt.Before(cut) && r.Status != "ready" && r.Status != "running" && r.Status != "scheduled" {
			continue
		}
		records = append(records, r)
	}
	n.Records = records
	days := n.Days[:0]
	date := cut.Format("2006-01-02")
	for _, d := range n.Days {
		if d.Date >= date {
			days = append(days, d)
		}
	}
	n.Days = days
}
func expire(n *snapshot, now time.Time) bool {
	changed := false
	for i := range n.Records {
		r := &n.Records[i]
		if r.Status == "scheduled" && now.After(r.DueAt.Add(10*time.Minute)) {
			change(r, "expired", "scheduled opportunity elapsed", now)
			changed = true
		}
	}
	return changed
}
func (s *Store) EnsureDay(role string, now time.Time, loc *time.Location) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(role); err != nil {
		return err
	}
	if loc == nil || now.IsZero() {
		return ErrInvalid
	}
	now = now.In(loc)
	n := s.clone()
	dirty := expire(&n, now)
	p := setting(n, role)
	date := now.Format("2006-01-02")
	for _, d := range n.Days {
		if d.RoleID == role && d.Date == date {
			if dirty {
				return s.commit(n)
			}
			return nil
		}
	}
	if !p.Enabled {
		if dirty {
			return s.commit(n)
		}
		return nil
	}
	trim(&n, now)
	if len(n.Days) >= 8192 || len(n.Records)+p.VisitsPerDay > 1024 {
		return ErrLimit
	}
	roll, err := s.random(100)
	if err != nil {
		return err
	}
	skip := roll >= p.ScanProbability
	a, _ := minute(p.WindowStart)
	b, _ := minute(p.WindowEnd)
	roleHash := sha256.Sum256([]byte(role))
	for i := 0; i < p.VisitsPerDay; i++ {
		// Stratification spreads visits across the window; each slot is drawn once.
		lo := a*60 + (b-a)*60*i/p.VisitsPerDay
		hi := a*60 + (b-a)*60*(i+1)/p.VisitsPerDay
		offset, e := s.random(hi - lo)
		if e != nil {
			return e
		}
		seconds := lo + offset
		due := time.Date(now.Year(), now.Month(), now.Day(), seconds/3600, seconds/60%60, seconds%60, 0, loc)
		query, e := s.random(len(p.Queries))
		if e != nil {
			return e
		}
		r := Record{ID: fmt.Sprintf("%x_%s_%d", roleHash[:12], now.Format("20060102"), i), RoleID: role, Revision: 1, PolicyRevision: p.Revision, Status: "scheduled", DueAt: due, CreatedAt: now, UpdatedAt: now, Query: p.Queries[query]}
		if skip {
			r.Status = "skipped"
			r.Reason = "daily probability roll skipped discovery"
		} else if due.Before(now) {
			r.Status = "expired"
			r.Reason = "slot elapsed before schedule creation"
		}
		n.Records = append(n.Records, r)
	}
	n.Days = append(n.Days, day{Date: date, RoleID: role})
	return s.commit(n)
}
func (s *Store) ClaimDue(role string, now time.Time, loc *time.Location) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(role); err != nil {
		return Record{}, err
	}
	if loc == nil || now.IsZero() {
		return Record{}, ErrInvalid
	}
	now = now.In(loc)
	n := s.clone()
	if expire(&n, now) {
		if err := s.commit(n); err != nil {
			return Record{}, err
		}
		n = s.clone()
	}
	p := setting(n, role)
	a, _ := minute(p.WindowStart)
	b, _ := minute(p.WindowEnd)
	m := now.Hour()*60 + now.Minute()
	if !p.Enabled || m < a || m >= b || !n.LastClaim.IsZero() && now.Sub(n.LastClaim) < 3*time.Hour {
		return Record{}, ErrBlocked
	}
	for _, r := range n.Records {
		if r.Status == "ready" || r.Status == "running" {
			return Record{}, ErrBlocked
		}
	}
	date := now.Format("2006-01-02")
	total := 0
	own := -1
	for i, d := range n.Days {
		if d.Date == date {
			total += d.Claims
			if d.RoleID == role {
				own = i
			}
		}
	}
	if total >= 3 || own >= 0 && n.Days[own].Claims >= p.DailyLimit {
		return Record{}, ErrBlocked
	}
	if own < 0 {
		return Record{}, ErrNotFound
	}
	idx := -1
	for i, r := range n.Records {
		if r.RoleID == role && r.Status == "scheduled" && r.PolicyRevision == p.Revision && r.DueAt.In(loc).Format("2006-01-02") == date && !r.DueAt.After(now) && !now.After(r.DueAt.Add(10*time.Minute)) && (idx < 0 || r.DueAt.Before(n.Records[idx].DueAt)) {
			idx = i
		}
	}
	if idx < 0 {
		return Record{}, ErrNotFound
	}
	change(&n.Records[idx], "running", "discovery attempt reserved", now)
	n.Days[own].Claims++
	n.LastClaim = now
	if err := s.commit(n); err != nil {
		return Record{}, err
	}
	return copyRecord(n.Records[idx]), nil
}
func (s *Store) Finish(role, id string, revision int, result Result) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(role); err != nil {
		return Record{}, err
	}
	if !validResult(result) {
		return Record{}, ErrInvalid
	}
	n := s.clone()
	for i := range n.Records {
		r := &n.Records[i]
		if r.RoleID != role || r.ID != id {
			continue
		}
		if r.Revision != revision || r.Status != "running" {
			return Record{}, ErrConflict
		}
		p := setting(n, role)
		status, reason := result.Status, result.Reason
		if !p.Enabled || p.Revision != r.PolicyRevision {
			status = "canceled"
			reason = "discovery settings changed"
		}
		change(r, status, reason, s.now())
		r.SearchCount = result.SearchCount
		r.ReadCount = result.ReadCount
		r.ModelCalls = result.ModelCalls
		r.SessionID = ""
		r.MessageIndex = nil
		if status == "ready" && result.MessageIndex != nil {
			r.SessionID = result.SessionID
			index := *result.MessageIndex
			r.MessageIndex = &index
		}
		if status != "canceled" {
			r.Content = result.Content
			r.Sources = append([]Source(nil), result.Sources...)
		}
		if err := s.commit(n); err != nil {
			return Record{}, err
		}
		return copyRecord(*r), nil
	}
	return Record{}, ErrNotFound
}

// PrepareDelivery records a non-visible delivery intent after the model
// decision and final policy check. The caller must append the assistant message
// before CommitDelivery makes the share eligible for inbox and push delivery.
func (s *Store) PrepareDelivery(role, id string, revision int, result Result) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(role); err != nil {
		return Record{}, err
	}
	if !validResult(result) || result.Status != "ready" || result.SessionID == "" || result.MessageIndex == nil {
		return Record{}, ErrInvalid
	}
	n := s.clone()
	for i := range n.Records {
		r := &n.Records[i]
		if r.RoleID != role || r.ID != id {
			continue
		}
		if r.Revision != revision || r.Status != "running" {
			return Record{}, ErrConflict
		}
		p := setting(n, role)
		status, reason := "delivering", result.Reason
		if !p.Enabled || p.Revision != r.PolicyRevision {
			status = "canceled"
			reason = "discovery settings changed"
		}
		change(r, status, reason, s.now())
		r.SearchCount = result.SearchCount
		r.ReadCount = result.ReadCount
		r.ModelCalls = result.ModelCalls
		r.Content = ""
		r.Sources = nil
		r.SessionID = ""
		r.MessageIndex = nil
		if status == "delivering" {
			r.Content = result.Content
			r.Sources = append([]Source(nil), result.Sources...)
			r.SessionID = result.SessionID
			index := *result.MessageIndex
			r.MessageIndex = &index
		}
		if err := s.commit(n); err != nil {
			return Record{}, err
		}
		return copyRecord(*r), nil
	}
	return Record{}, ErrNotFound
}

func (s *Store) CommitDelivery(role, id string, revision int) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(role); err != nil {
		return Record{}, err
	}
	n := s.clone()
	for i := range n.Records {
		r := &n.Records[i]
		if r.RoleID != role || r.ID != id {
			continue
		}
		if r.Status != "delivering" || r.Revision != revision {
			return Record{}, ErrConflict
		}
		r.Status = "ready"
		r.UpdatedAt = s.now()
		r.Revision++
		if err := s.commit(n); err != nil {
			return Record{}, err
		}
		return copyRecord(*r), nil
	}
	return Record{}, ErrNotFound
}

// FailDelivery clears a prepared or ready share if committing its assistant
// message failed. Search/model charges remain part of the run.
func (s *Store) FailDelivery(role, id string, revision int, reason string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(role); err != nil {
		return Record{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return Record{}, ErrInvalid
	}
	n := s.clone()
	for i := range n.Records {
		r := &n.Records[i]
		if r.RoleID != role || r.ID != id {
			continue
		}
		if r.Status != "delivering" && r.Status != "ready" || r.Revision != revision {
			return Record{}, ErrConflict
		}
		change(r, "failed", reason, s.now())
		r.Content = ""
		r.Sources = nil
		r.SessionID = ""
		r.MessageIndex = nil
		if err := s.commit(n); err != nil {
			return Record{}, err
		}
		return copyRecord(*r), nil
	}
	return Record{}, ErrNotFound
}
func (s *Store) Records(role string) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(role); err != nil {
		return nil, err
	}
	out := []Record{}
	for _, r := range s.state.Records {
		if r.RoleID == role {
			if r.Status == "delivering" {
				r.Content = ""
				r.Sources = nil
				r.SessionID = ""
				r.MessageIndex = nil
			}
			out = append(out, copyRecord(r))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}
func (s *Store) Mark(role, id, status string, revision int) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(role); err != nil {
		return Record{}, err
	}
	if status != "seen" && status != "canceled" {
		return Record{}, ErrInvalid
	}
	n := s.clone()
	for i := range n.Records {
		r := &n.Records[i]
		if r.RoleID != role || r.ID != id {
			continue
		}
		if r.Revision != revision {
			return Record{}, ErrConflict
		}
		if status == "seen" && r.Status != "ready" || status == "canceled" && r.Status != "ready" && r.Status != "scheduled" && r.Status != "running" {
			return Record{}, ErrConflict
		}
		change(r, status, "user updated topic", s.now())
		if err := s.commit(n); err != nil {
			return Record{}, err
		}
		return copyRecord(*r), nil
	}
	return Record{}, ErrNotFound
}
func (s *Store) DeleteRecord(role, id string, revision int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(role); err != nil {
		return err
	}
	n := s.clone()
	for i, r := range n.Records {
		if r.RoleID == role && r.ID == id {
			if r.Revision != revision {
				return ErrConflict
			}
			if r.Status == "running" || r.Status == "delivering" {
				return ErrBlocked
			}
			n.Records = append(n.Records[:i], n.Records[i+1:]...)
			return s.commit(n)
		}
	}
	return ErrNotFound
}
