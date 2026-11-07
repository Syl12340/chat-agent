//go:build !windows

package runlog

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func matchTreeOwnership(root, reference string) error {
	info, err := os.Stat(reference)
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("data directory ownership is unavailable")
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("runtime registry contains a symbolic link")
		}
		current, err := entry.Info()
		if err != nil {
			return err
		}
		state, ok := current.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New("runtime ownership is unavailable")
		}
		if state.Uid == owner.Uid && state.Gid == owner.Gid {
			return nil
		}
		return os.Chown(path, int(owner.Uid), int(owner.Gid))
	})
}
