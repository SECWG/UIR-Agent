//go:build !windows

package terminal

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
)

type Process struct {
	file *os.File
	cmd  *exec.Cmd
}

func Available() bool { return true }

func Open(shell string, cols, rows int) (*Process, error) {
	args := []string{"-i"}
	if shell == "pwsh" || shell == "powershell" {
		args = []string{"-NoLogo"}
	}
	cmd := exec.Command(shell, args...)
	cmd.Env = Environment()
	cmd.Dir, _ = os.UserHomeDir()
	file, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, err
	}
	go func() { _ = cmd.Wait() }()
	return &Process{file: file, cmd: cmd}, nil
}

func (t *Process) Read(p []byte) (int, error)  { return t.file.Read(p) }
func (t *Process) Write(p []byte) (int, error) { return t.file.Write(p) }
func (t *Process) Resize(cols, rows int) error {
	return pty.Setsize(t.file, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

func (t *Process) Close() {
	_ = syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
	_ = t.file.Close()
}
