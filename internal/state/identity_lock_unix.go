//go:build !windows

package state

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func tryIdentityLock(f *os.File) (bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}

func releaseIdentityLock(f *os.File) { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }
func replaceIdentityFile(source, target string) error {
	if err := os.Rename(source, target); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
