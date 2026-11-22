package management

import (
	"errors"

	"golang.org/x/sys/windows"
)

// Windows 的 / 表示“此电脑”；枚举盘符不探测介质，避免未就绪磁盘阻塞导航。
func browseFileRoot(request fileRequest) (any, bool, error) {
	if request.Path != "/" && request.Path != `\` {
		return nil, false, nil
	}
	if request.Operation != "list" || request.Offset > remoteDirectoryLimit {
		return nil, true, errors.New("REMOTE_PATH_INVALID")
	}
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil, true, errors.New("REMOTE_PATH_UNAVAILABLE")
	}
	entries := volumeEntries(mask)
	entries = entries[min(int(request.Offset), len(entries)):]
	return map[string]any{"path": "/", "parent": nil, "entries": entries, "nextOffset": nil}, true, nil
}

func volumeEntries(mask uint32) []remoteEntry {
	entries := make([]remoteEntry, 0, 26)
	for letter := byte('A'); letter <= 'Z'; letter++ {
		if mask&(1<<(letter-'A')) != 0 {
			path := string(letter) + `:\`
			entries = append(entries, remoteEntry{Name: path, Path: path, Kind: "directory"})
		}
	}
	return entries
}
