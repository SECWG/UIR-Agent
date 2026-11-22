package diagnostics

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var crashLogNames = []string{"agent-crash.log", "agent-crash.log.1", "agent-crash.log.2"}

// OpenCrashLog runs only while the caller owns the instance's state lock.
// The runtime writes directly to a duplicated file handle, even if a panic
// unwinds ordinary logging. Rotate only nonempty reports on the next startup;
// repeated normal starts must not discard the last crash.
func OpenCrashLog(dir string) (*os.File, error) {
	var reportSize int64
	for i, name := range crashLogNames {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err == nil {
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("crash log is not a regular file: %s", name)
			}
			if i == 0 {
				reportSize = info.Size()
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	if reportSize > 0 {
		if err := os.Remove(filepath.Join(dir, crashLogNames[2])); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for i := 1; i >= 0; i-- {
			if err := os.Rename(filepath.Join(dir, crashLogNames[i]), filepath.Join(dir, crashLogNames[i+1])); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		}
	}
	return os.OpenFile(filepath.Join(dir, crashLogNames[0]), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
}
