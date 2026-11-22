//go:build linux || darwin

package state

import (
	"syscall"
)

func AvailableBytes(path string) (uint64, error) {
	var s syscall.Statfs_t
	err := syscall.Statfs(path, &s)
	return uint64(s.Bavail) * uint64(s.Bsize), err
}
