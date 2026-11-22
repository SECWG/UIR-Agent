//go:build windows && !amd64

package terminal

import "errors"

func winPTYAvailable() bool { return false }

func openWinPTY(path, command, home string, env []string, cols, rows int) (windowsTerminal, error) {
	return nil, errors.New("interactive terminal unavailable on this architecture")
}
