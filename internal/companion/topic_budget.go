package companion

import (
	"time"

	"pocket-agent/internal/storage"
)

// TopicAttempt is a budget receipt, not a message or long-term personal fact.
type TopicAttempt struct {
	RoleID         string    `json:"role_id"`
	AttemptedAt    time.Time `json:"attempted_at"`
	PolicyRevision int       `json:"policy_revision"`
}

// The topic setting grants its own opt-in. Normal greetings need not be enabled;
// their quiet/snooze, spacing and attempt limits still constrain discovery.
func (s *LifeStore) topicAllowed(role string, now time.Time, loc *time.Location) error {
	if err := s.ready(role); err != nil {
		return err
	}
	p, ok := s.state.Settings[role]
	if !ok {
		p = defaultCare(role)
	}
	if loc == nil || now.IsZero() || quiet(p, now, loc) || (p.SnoozeUntil != nil && now.Before(*p.SnoozeUntil)) {
		return ErrCareBlocked
	}
	if s.state.Attempts[role+"|"+now.In(loc).Format("2006-01-02")] >= p.DailyLimit {
		return ErrCareBlocked
	}
	for _, old := range s.state.Care {
		attempt := old.AttemptedAt
		if attempt == nil && (old.Status == "generating" || old.Status == "ready" || old.Status == "seen" || old.Status == "failed" || old.Status == "interrupted") {
			copy := old.CreatedAt
			attempt = &copy
		}
		if old.RoleID == role && attempt != nil && now.Before(attempt.Add(time.Duration(p.MinIntervalMinutes)*time.Minute)) {
			return ErrCareBlocked
		}
	}
	for _, old := range s.state.TopicAttempts {
		if old.RoleID == role && now.Before(old.AttemptedAt.Add(time.Duration(p.MinIntervalMinutes)*time.Minute)) {
			return ErrCareBlocked
		}
	}
	return nil
}

func (s *LifeStore) TopicAllowed(role string, now time.Time, loc *time.Location) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.topicAllowed(role, now, loc)
}

// ReserveTopicAttempt is persisted before search or model work. Returning an
// error never grants permission to continue; an existing ID is not replayable.
func (s *LifeStore) ReserveTopicAttempt(role, id string, now time.Time, loc *time.Location) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !storage.ValidID(id) {
		return ErrInvalid
	}
	if err := s.topicAllowed(role, now, loc); err != nil {
		return err
	}
	if _, ok := s.state.TopicAttempts[id]; ok {
		return ErrCareBlocked
	}
	next := cloneLife(s.state)
	if next.TopicAttempts == nil {
		next.TopicAttempts = map[string]TopicAttempt{}
	}
	cutoff := now.AddDate(0, 0, -30)
	for key, old := range next.TopicAttempts {
		if old.AttemptedAt.Before(cutoff) {
			delete(next.TopicAttempts, key)
		}
	}
	if len(next.TopicAttempts) >= 4096 {
		return ErrLifeLimit
	}
	p, ok := s.state.Settings[role]
	if !ok {
		p = defaultCare(role)
	}
	next.TopicAttempts[id] = TopicAttempt{RoleID: role, AttemptedAt: now.UTC(), PolicyRevision: p.Revision}
	key := role + "|" + now.In(loc).Format("2006-01-02")
	next.Attempts[key]++
	for key := range next.Attempts {
		if len(key) > 11 && key[len(key)-10:] < cutoff.In(loc).Format("2006-01-02") {
			delete(next.Attempts, key)
		}
	}
	return s.save(next)
}

func (s *LifeStore) TopicStillAllowed(role, id string, now time.Time, loc *time.Location) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return err
	}
	attempt, ok := s.state.TopicAttempts[id]
	if !ok || attempt.RoleID != role {
		return ErrCareBlocked
	}
	p, ok := s.state.Settings[role]
	if !ok {
		p = defaultCare(role)
	}
	if loc == nil || p.Revision != attempt.PolicyRevision || quiet(p, now, loc) || (p.SnoozeUntil != nil && now.Before(*p.SnoozeUntil)) {
		return ErrCareBlocked
	}
	return nil
}
