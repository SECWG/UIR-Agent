//go:build !windows

package management

func browseFileRoot(fileRequest) (any, bool, error) { return nil, false, nil }
