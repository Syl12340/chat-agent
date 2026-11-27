package deviceauth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"chat-agent/internal/storage"
)

// Slots keep the stable, non-secret client identity separate from signing
// credentials so old binaries can still read devices.json during rollback.
func (s *Store) loadSlots() error {
	data, err := os.ReadFile(filepath.Join(s.dir, "device-slots.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var slots map[string]string
	if len(data) > 80<<10 || json.Unmarshal(data, &slots) != nil || len(slots) > maxDevices {
		return errors.New("invalid device slots")
	}
	for slot, id := range slots {
		if !hexID(slot) || !hexID(id) {
			return errors.New("invalid device slot")
		}
		if _, ok := s.devices[id]; !ok {
			// The two files are written one after the other, so a crash between
			// them - or a rollback that itself failed - leaves a slot pointing at
			// a credential that no longer exists. A slot is only a derived
			// mapping that the client rebinds on its next registration, so drop
			// it. Refusing to open the store over this would keep the whole
			// service down until someone edited the file by hand.
			delete(slots, slot)
		}
	}
	if slots != nil {
		s.slots = slots
	}
	return nil
}

func cloneSlots(source map[string]string) map[string]string {
	result := make(map[string]string, len(source)+1)
	for key, value := range source {
		result[key] = value
	}
	return result
}

func (s *Store) saveSlots(slots map[string]string) error {
	data, err := json.Marshal(slots)
	if err != nil {
		return err
	}
	return storage.Replace(filepath.Join(s.dir, "device-slots.json"), append(data, '\n'))
}

// bindSlotLocked is called only after a successful signature proof. That proof
// may safely displace an anonymous pending registration that guessed the same
// slot, while an existing trusted or revoked binding remains protected.
func (s *Store) bindSlotLocked(slotID, id string) error {
	if slotID == "" {
		return nil
	}
	if !hexID(slotID) || !hexID(id) {
		return ErrInvalid
	}
	previousID := s.slots[slotID]
	if previousID == id {
		return nil
	}
	updated := s.devices
	changedDevices := false
	if previousID != "" {
		previous, ok := s.devices[previousID]
		if !ok {
			return ErrConflict
		}
		if previous.Status == "approved" {
			return ErrKeyChanged
		}
		if previous.Status == "revoked" {
			return ErrRevoked
		}
		updated = make(map[string]record, len(s.devices))
		changedDevices = true
		for key, value := range s.devices {
			if key != previousID {
				updated[key] = value
			}
		}
		if err := s.save(updated, s.ownerClaimed); err != nil {
			return err
		}
	}
	newSlots := cloneSlots(s.slots)
	newSlots[slotID] = id
	if err := s.saveSlots(newSlots); err != nil {
		if changedDevices {
			// Compensating, not atomic. If this also fails the two files really
			// are inconsistent, so report both: a caller that sees only the
			// write error would retry against a store that no longer matches
			// what it believes it wrote.
			if rollbackErr := s.save(s.devices, s.ownerClaimed); rollbackErr != nil {
				return errors.Join(err, rollbackErr)
			}
		}
		return err
	}
	s.devices, s.slots = updated, newSlots
	return nil
}
