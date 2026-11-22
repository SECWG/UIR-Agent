package management

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

const remoteDirectoryLimit = 100000
const remoteDirectoryBytes = 16 * 1024 * 1024

// 与平台 SSH 浏览保持相同的数字段、大小写和原始名称排序规则。
func compareRemoteNames(a, b string) int {
	left, right := strings.ToLower(a), strings.ToLower(b)
	for len(left) > 0 && len(right) > 0 {
		x, y := namePart(left), namePart(right)
		left, right = left[len(x):], right[len(y):]
		if isNameDigit(x[0]) && isNameDigit(y[0]) {
			x, y = strings.TrimLeft(x, "0"), strings.TrimLeft(y, "0")
			if len(x) != len(y) {
				return len(x) - len(y)
			}
		}
		if compared := strings.Compare(x, y); compared != 0 {
			return compared
		}
	}
	if left != "" {
		return 1
	}
	if right != "" {
		return -1
	}
	return strings.Compare(a, b)
}

func isNameDigit(b byte) bool { return b >= '0' && b <= '9' }
func namePart(s string) string {
	n := 1
	for n < len(s) && isNameDigit(s[n]) == isNameDigit(s[0]) {
		n++
	}
	return s[:n]
}

// 每条管理连接只复用最近一个目录；第一页重读，翻页最多复用 30 秒。
type directoryCache struct {
	mu      sync.Mutex
	path    string
	info    os.FileInfo
	expires time.Time
	entries []os.DirEntry
}

func (c *directoryCache) read(ctx context.Context, f *os.File, info os.FileInfo, offset int64) ([]os.DirEntry, error) {
	if ctx.Err() != nil {
		return nil, errors.New("REMOTE_READ_TIMEOUT")
	}
	c.mu.Lock()
	if offset == 0 || time.Now().After(c.expires) {
		c.entries, c.info = nil, nil
	}
	if c.info != nil && c.path == f.Name() && os.SameFile(c.info, info) {
		entries := c.entries
		c.mu.Unlock()
		return entries, nil
	}
	c.mu.Unlock()
	deadline := time.Now().Add(15 * time.Second)
	entries := make([]os.DirEntry, 0)
	bytes := 0
	for {
		if ctx.Err() != nil || time.Now().After(deadline) {
			return nil, errors.New("REMOTE_READ_TIMEOUT")
		}
		page, err := f.ReadDir(256)
		for _, entry := range page {
			bytes += 2*len(entry.Name()) + len(f.Name()) + 97
		}
		if len(entries)+len(page) > remoteDirectoryLimit || bytes > remoteDirectoryBytes {
			return nil, errors.New("REMOTE_DIRECTORY_TOO_LARGE")
		}
		entries = append(entries, page...)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.New("REMOTE_READ_FAILED")
		}
	}
	slices.SortFunc(entries, func(a, b os.DirEntry) int {
		if a.IsDir() != b.IsDir() {
			if a.IsDir() {
				return -1
			}
			return 1
		}
		return compareRemoteNames(a.Name(), b.Name())
	})
	if ctx.Err() != nil || time.Now().After(deadline) {
		return nil, errors.New("REMOTE_READ_TIMEOUT")
	}
	c.mu.Lock()
	c.path, c.info, c.expires, c.entries = f.Name(), info, time.Now().Add(30*time.Second), entries
	c.mu.Unlock()
	return entries, nil
}
