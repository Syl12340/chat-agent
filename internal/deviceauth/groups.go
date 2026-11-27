package deviceauth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"chat-agent/internal/storage"
)

// The sidecar stores administrative groupings only. Signing credentials keep
// their original IDs and v1 layout, so both keys and old sessions still work.
func (s *Store) loadGroups() error {
	data, err := os.ReadFile(filepath.Join(s.dir, "device-groups.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var groups map[string]string
	if len(data) > 80<<10 || json.Unmarshal(data, &groups) != nil || len(groups) >= maxDevices {
		return errors.New("invalid device groups")
	}
	for member, root := range groups {
		if _, ok := s.devices[member]; !ok {
			return errors.New("invalid device group member")
		}
		if _, ok := s.devices[root]; !ok || groups[root] != "" {
			return errors.New("invalid device group root")
		}
	}
	if groups != nil {
		s.groups = groups
	}
	return nil
}

func (s *Store) deviceGroup(id string) string {
	if root := s.groups[id]; root != "" {
		return root
	}
	return id
}

func (s *Store) groupIDs(id string) []string {
	root := s.deviceGroup(id)
	ids := []string{root}
	for member, parent := range s.groups {
		if parent == root {
			ids = append(ids, member)
		}
	}
	sort.Strings(ids)
	return ids
}

func (s *Store) GroupIDs(id string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.groupIDs(id)
}

// MergeGroups is an explicit admin decision about already approved keys.
// Merging does not create trust, rotate credentials or invalidate sessions.
func (s *Store) MergeGroups(from, into string, revision, targetRevision int, actor string, owner bool) error {
	if !hexID(from) || !hexID(into) || from == into || revision < 1 || targetRevision < 1 {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if err := s.authorizeAdmin(actor, owner); err != nil {
		return err
	}
	for id, expect := range map[string]int{from: revision, into: targetRevision} {
		item, err := s.approved(id)
		if err != nil {
			return err
		}
		if item.Revision != expect {
			return ErrConflict
		}
	}
	root := s.deviceGroup(into)
	if s.deviceGroup(from) == root {
		return nil
	}
	groups := make(map[string]string, len(s.groups)+1)
	for member, parent := range s.groups {
		groups[member] = parent
	}
	for _, id := range s.groupIDs(from) {
		groups[id] = root
	}
	data, err := json.Marshal(groups)
	if err != nil {
		return err
	}
	if err := storage.Replace(filepath.Join(s.dir, "device-groups.json"), append(data, '\n')); err != nil {
		return err
	}
	s.groups = groups
	return nil
}

// Resolve only checks caller-supplied public-key digests. It never registers a
// key or returns names, public keys, or any other enrolled IDs.
func (s *Store) Resolve(candidates []string) (string, error) {
	if len(candidates) > 40 {
		return "", ErrInvalid
	}
	for _, id := range candidates {
		if !hexID(id) {
			return "", ErrInvalid
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", ErrClosed
	}
	// An already approved legacy key takes priority over a pending one from a
	// previous connection. A revoked key takes priority over a fresh request.
	for _, status := range []string{"approved", "revoked", "pending"} {
		for _, id := range candidates {
			if s.devices[id].Status == status {
				return id, nil
			}
		}
	}
	return "", nil
}
