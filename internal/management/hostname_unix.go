//go:build linux || darwin

package management

import (
	"os"
)

func Hostname() (string, error) { return os.Hostname() }
