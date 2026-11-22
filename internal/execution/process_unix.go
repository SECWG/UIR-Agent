//go:build linux || darwin

package execution

import (
	"errors"
	"os/exec"
	"syscall"
)

func StartProcess(cmd *exec.Cmd) (func() error, func(), error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	kill := func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil // The process group has already exited.
		}
		return err
	}
	return kill, func() { _ = kill() }, nil
}
