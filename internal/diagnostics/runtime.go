package diagnostics

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const runtimeLogLimit = 5 << 20

var runtimeLogNames = []string{"agent.log", "agent.log.1", "agent.log.2"}

// RuntimeLog is opened only by the process holding the existing state lock.
// Keep a small, bounded diagnostic history; identity and task data stay separate.
type RuntimeLog struct {
	mu    sync.Mutex
	dir   string
	file  *os.File
	size  int64
	limit int64
}

func OpenRuntimeLog(dir string) (*RuntimeLog, error) {
	for _, name := range runtimeLogNames {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err == nil && !info.Mode().IsRegular() {
			return nil, fmt.Errorf("runtime log is not a regular file: %s", name)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	l := &RuntimeLog{dir: dir, limit: runtimeLogLimit}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *RuntimeLog) open() error {
	f, err := os.OpenFile(filepath.Join(l.dir, "agent.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.file, l.size = f, info.Size()
	return nil
}

func (l *RuntimeLog) rotate() error {
	if err := l.file.Close(); err != nil {
		return err
	}
	l.file = nil
	if err := os.Remove(filepath.Join(l.dir, "agent.log.2")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for i := 1; i >= 0; i-- {
		if err := os.Rename(filepath.Join(l.dir, runtimeLogNames[i]), filepath.Join(l.dir, runtimeLogNames[i+1])); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return l.open()
}

func (l *RuntimeLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return 0, os.ErrClosed
	}
	written := 0
	for len(p) > 0 {
		if l.size >= l.limit {
			if err := l.rotate(); err != nil {
				return written, err
			}
		}
		n := min(len(p), int(l.limit-l.size))
		count, err := l.file.Write(p[:n])
		written += count
		l.size += int64(count)
		if err != nil {
			return written, err
		}
		p = p[count:]
	}
	return written, nil
}

func (l *RuntimeLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}
