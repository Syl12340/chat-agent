package turns

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"pocket-agent/internal/media"
	"pocket-agent/internal/storage"
	"strings"
)

func cloneTurn(turn Turn) Turn {
	turn.Images = append([]media.Image(nil), turn.Images...)
	turn.Tools = append([]ToolEvent(nil), turn.Tools...)
	return turn
}
func validOptions(model, thinking string, images []media.Image) bool {
	if len(model) > 256 || strings.TrimSpace(model) != model || strings.ContainsAny(model, "\r\n\x00") || len(images) > 3 {
		return false
	}
	switch thinking {
	case "", "off", "medium", "high", "ultra":
	default:
		return false
	}
	seen := map[string]bool{}
	for _, image := range images {
		if !storage.ValidID(image.ID) || seen[image.ID] || image.MIMEType != "image/png" && image.MIMEType != "image/jpeg" || image.Size < 1 || image.Size > 4<<20 || image.Width < 1 || image.Height < 1 || image.Width > 4096 || image.Height > 4096 || int64(image.Width)*int64(image.Height) > 12_000_000 || image.SHA256 != "" && !media.ValidSHA256(image.SHA256) || image.DataURL != "" {
			return false
		}
		seen[image.ID] = true
	}
	return true
}
func optionsDigest(content, mode, model, thinking string, images []media.Image) string {
	if model == "" && thinking == "" && len(images) == 0 {
		return digest(content, mode)
	}
	type imageIdentity struct {
		ID       string `json:"id"`
		MIMEType string `json:"mime_type"`
		Size     int    `json:"size"`
		Width    int    `json:"width"`
		Height   int    `json:"height"`
	}
	identities := make([]imageIdentity, 0, len(images))
	for _, image := range images {
		identities = append(identities, imageIdentity{image.ID, image.MIMEType, image.Size, image.Width, image.Height})
	}
	value, _ := json.Marshal(struct {
		Content, Mode, Model, Thinking string
		Images                         []imageIdentity
	}{content, mode, model, thinking, identities})
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:])
}

// AllImages includes the bounded ledger beyond its last 32 detailed UI rows.
// Callers also inspect transcript references before pruning session media.
func (s *Store) AllImages(id string) ([]media.Image, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.load(id)
	if err != nil {
		return nil, err
	}
	out := []media.Image{}
	seen := map[string]bool{}
	for _, record := range st.Items {
		for _, image := range record.Images {
			if !seen[image.ID] {
				seen[image.ID] = true
				out = append(out, image)
			}
		}
	}
	return out, nil
}

// RemoveBatch removes the complete merged response's ledger in one replace.
func (s *Store) RemoveBatch(id, rid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.load(id)
	if err != nil {
		return err
	}
	var target *record
	for i := range st.Items {
		if st.Items[i].ID == rid {
			target = &st.Items[i]
			break
		}
	}
	if target == nil {
		return ErrNotFound
	}
	next := clone(st)
	next.Items = next.Items[:0]
	for _, r := range st.Items {
		remove := r.ID == rid || target.BatchID != "" && r.BatchID == target.BatchID
		if remove {
			if r.Status == "running" {
				return ErrConflict
			}
			continue
		}
		next.Items = append(next.Items, r)
	}
	for i := range next.Items {
		next.Items[i].Sequence = i + 1
	}
	return s.save(next)
}
