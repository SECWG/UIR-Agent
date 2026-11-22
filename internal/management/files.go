package management

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/SECWG/UIR-Agent/internal/protocol"
)

type fileRequest struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Operation string `json:"operation"`
	Path      string `json:"path"`
	Offset    int64  `json:"offset"`
}

type remoteEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	Size     int64  `json:"size"`
	Modified string `json:"modifiedAt"`
	Mode     string `json:"mode"`
}

// 当前目录按名称排序后分页；不递归子目录，不将浏览动作写成执行任务。
func browseFiles(ctx context.Context, private string, request fileRequest, directories *directoryCache) (any, error) {
	if ctx.Err() != nil {
		return nil, errors.New("REMOTE_READ_TIMEOUT")
	}
	if !protocol.ValidID(request.ID) ||
		request.Offset < 0 ||
		request.Offset > 1<<40 ||
		len(request.Path) > 4096 ||
		strings.ContainsRune(request.Path, 0) {
		return nil, errors.New("REMOTE_PATH_INVALID")
	}
	if result, handled, err := browseFileRoot(request); handled {
		return result, err
	}
	path := request.Path
	// 裸盘符表示盘符根目录，不使用 Windows 为各盘保存的相对工作目录。
	if runtime.GOOS == "windows" &&
		len(path) == 2 &&
		path[1] == ':' &&
		((path[0] >= 'A' &&
			path[0] <= 'Z') ||
			(path[0] >= 'a' &&
				path[0] <= 'z')) {
		path += `\`
	}
	if path == "" {
		path, _ = os.UserHomeDir()
	}
	if !filepath.IsAbs(path) {
		return nil, errors.New("REMOTE_PATH_INVALID")
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, errors.New("REMOTE_PATH_UNAVAILABLE")
	}
	if ctx.Err() != nil {
		return nil, errors.New("REMOTE_READ_TIMEOUT")
	}
	secret, err := filepath.EvalSymlinks(private)
	if err != nil {
		return nil, errors.New("REMOTE_PATH_UNAVAILABLE")
	}
	rel, err := filepath.Rel(secret, path)
	if err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))) {
		return nil, errors.New("REMOTE_PATH_PRIVATE")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, errors.New("REMOTE_PATH_UNAVAILABLE")
	}
	// 先排除设备和管道，避免打开特殊文件时阻塞文件处理名额。
	if request.Operation == "read" && !info.Mode().IsRegular() {
		return nil, errors.New("REMOTE_NOT_REGULAR_FILE")
	}
	if request.Operation != "read" && (request.Operation != "list" || !info.IsDir() || request.Offset > remoteDirectoryLimit) {
		return nil, errors.New("REMOTE_PATH_INVALID")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("REMOTE_PATH_UNAVAILABLE")
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return nil, errors.New("REMOTE_PATH_UNAVAILABLE")
	}
	if request.Operation == "read" {
		if !info.Mode().IsRegular() {
			return nil, errors.New("REMOTE_NOT_REGULAR_FILE")
		}
		data := make([]byte, 32768)
		n, err := f.ReadAt(data, request.Offset)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, errors.New("REMOTE_READ_FAILED")
		}
		var next any
		if request.Offset+int64(n) < info.Size() {
			next = request.Offset + int64(n)
		}
		return map[string]any{
			"path":       path,
			"size":       info.Size(),
			"modifiedAt": info.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
			"data":       base64.StdEncoding.EncodeToString(data[:n]),
			"offset":     request.Offset,
			"nextOffset": next,
		}, nil
	}
	if request.Operation != "list" || !info.IsDir() || request.Offset > remoteDirectoryLimit {
		return nil, errors.New("REMOTE_PATH_INVALID")
	}
	entries, err := directories.read(ctx, f, info, request.Offset)
	if err != nil {
		return nil, err
	}
	var next any
	start := min(int(request.Offset), len(entries))
	end := min(start+100, len(entries))
	if end < len(entries) {
		next = int64(end)
	}
	entries = entries[start:end]
	result := make([]remoteEntry, 0, len(entries))
	for _, entry := range entries {
		if ctx.Err() != nil {
			return nil, errors.New("REMOTE_READ_TIMEOUT")
		}
		item := remoteEntry{Name: entry.Name(), Path: filepath.Join(path, entry.Name()), Kind: "other"}
		if entry.Type()&os.ModeSymlink != 0 {
			item.Kind = "link"
		} else if entry.IsDir() {
			item.Kind = "directory"
		} else if entry.Type().IsRegular() {
			item.Kind = "file"
		}
		if stat, err := entry.Info(); err == nil {
			item.Size = stat.Size()
			item.Modified = stat.ModTime().UTC().Format("2006-01-02T15:04:05Z")
			item.Mode = stat.Mode().String()
		}
		result = append(result, item)
	}
	var parent any
	if up := filepath.Dir(path); up != path {
		parent = up
	} else if runtime.GOOS == "windows" {
		parent = "/"
	}
	return map[string]any{"path": path, "parent": parent, "entries": result, "nextOffset": next}, nil
}
