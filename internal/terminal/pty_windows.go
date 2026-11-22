//go:build windows

package terminal

import (
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/UserExistsError/conpty"
)

type windowsTerminal interface {
	io.ReadWriter
	Resize(cols, rows int) error
	Close()
}

type Process struct{ windowsTerminal }

// Capability detection is independent of the standard/legacy build profile.
var conPTYAvailable = conpty.IsConPtyAvailable

func Available() bool {
	return conPTYAvailable() || winPTYAvailable()
}

func Open(shell string, cols, rows int) (*Process, error) {
	path, err := exec.LookPath(shell)
	if err != nil {
		return nil, err
	}
	command := syscall.EscapeArg(path)
	if shell == "powershell" || shell == "pwsh" {
		command += " -NoLogo"
	}
	home, _ := os.UserHomeDir()
	if !conPTYAvailable() {
		backend, err := openWinPTY(path, command, home, Environment(), cols, rows)
		if err != nil {
			return nil, err
		}
		return &Process{backend}, nil
	}
	console, err := conpty.Start(command, conpty.ConPtyDimensions(cols, rows), conpty.ConPtyWorkDir(home), conpty.ConPtyEnv(Environment()))
	if err != nil {
		return nil, err
	}
	return &Process{&conPTYTerminal{console: console}}, nil
}

type conPTYTerminal struct {
	console *conpty.ConPty
	once    sync.Once
}

func (t *conPTYTerminal) Read(p []byte) (int, error)  { return t.console.Read(p) }
func (t *conPTYTerminal) Write(p []byte) (int, error) { return t.console.Write(p) }
func (t *conPTYTerminal) Resize(cols, rows int) error { return t.console.Resize(cols, rows) }
func (t *conPTYTerminal) Close() {
	t.once.Do(func() {
		// ClosePseudoConsole may flush output while closing.
		go func() { _, _ = io.Copy(io.Discard, t.console) }()
		_ = t.console.Close()
	})
}
