// Package storage contains the small, shared on-disk helpers used by chat-agent.
package storage

import (
	"errors"
	"os"
	"path/filepath"
)

func ValidID(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// Replace syncs a temporary file before replacing the destination in the same
// directory. Callers serialize writes. Directory power-loss durability is not claimed.
func Replace(path string, data []byte) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".chat-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = f.Close(); _ = os.Remove(name) }()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	n, err := f.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return errors.New("short file write")
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
