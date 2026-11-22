//go:build linux || darwin

package state

import (
	"os"
)

func ProtectDirectory(path string) error { return os.Chmod(path, 0700) }
