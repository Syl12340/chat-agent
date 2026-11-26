package companion

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"
	"unicode"

	"chat-agent/internal/storage"
)

// OfferChatContinuation puts one unfinished subject into the existing care
// ledger. Its source identity survives restart and uses the same care quota.
func (s *LifeStore) OfferChatContinuation(role, session, message, subject string, now time.Time, location *time.Location) (CareItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(role); err != nil {
		return CareItem{}, err
	}
	if !storage.ValidID(session) || !storage.ValidID(message) || !textValid(subject, 256, false) {
		return CareItem{}, ErrInvalid
	}
	p := s.state.Settings[role]
	if !p.Enabled || !p.AutoGenerate || !p.ContinueChats || quiet(p, now, location) || p.SnoozeUntil != nil && now.Before(*p.SnoozeUntil) {
		return CareItem{}, ErrCareBlocked
	}
	id := stableCareID("chat-continuation:" + role + ":" + session + ":" + message)
	day := now.In(location).Format("2006-01-02")
	tokens := continuationTokens(subject)
	for _, old := range s.state.Care {
		if old.RoleID != role || old.SourceMessageID == "" {
			continue
		}
		if old.ID == id {
			if old.Status == "due" {
				return old, nil
			}
			return CareItem{}, ErrCareBlocked
		}
		if old.CreatedAt.In(location).Format("2006-01-02") == day ||
			!old.CreatedAt.Before(now.Add(-72*time.Hour)) && continuationSimilarity(tokens, strings.Fields(old.SourceFingerprint)) >= 0.55 {
			return CareItem{}, ErrCareBlocked
		}
	}
	if len(s.state.Care) >= 2000 {
		return CareItem{}, ErrLifeLimit
	}
	item := CareItem{ID: id, RoleID: role, Title: "接着聊", SourceSessionID: session, SourceMessageID: message, SourceFingerprint: strings.Join(tokens, " "),
		Status: "due", Revision: 1, DueAt: now.UTC(), CreatedAt: now.UTC()}
	next := cloneLife(s.state)
	next.Care = append(next.Care, item)
	return item, s.save(next)
}

// Ported from Aurabot src/purpose/engine.js at 5eb5b946 and its MIT-licensed
// Surfcore antecedent. Compare Unicode bigrams and whole ASCII words by Jaccard.
func continuationSimilarity(left, right []string) float64 {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	common := 0
	for i, j := 0, 0; i < len(left) && j < len(right); {
		switch strings.Compare(left[i], right[j]) {
		case -1:
			i++
		case 1:
			j++
		default:
			common++
			i++
			j++
		}
	}
	return float64(common) / float64(len(left)+len(right)-common)
}

func continuationTokens(text string) []string {
	tokens := make(map[string]struct{})
	parts := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	for _, part := range parts {
		chars := []rune(part)
		ascii := len(chars) >= 2
		for i, r := range chars {
			ascii = ascii && (r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
			if i > 0 {
				tokens[string(chars[i-1:i+1])] = struct{}{}
			}
		}
		if ascii {
			tokens[part] = struct{}{}
		}
	}
	// Keep only fingerprints in the care ledger. The message text lives in the
	// private transcript and is read again only while the source is current.
	fingerprints := make([]string, 0, len(tokens))
	for token := range tokens {
		sum := sha256.Sum256([]byte(token))
		fingerprints = append(fingerprints, hex.EncodeToString(sum[:8]))
	}
	sort.Strings(fingerprints)
	return fingerprints
}
