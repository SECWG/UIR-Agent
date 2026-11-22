package terminal

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

type winPTYTerminal struct {
	api             *winPTYAPI
	dir             string
	console         uintptr
	input, output   *os.File
	helper, process windows.Handle
	mu              sync.Mutex // Serialize native resize/free; file I/O has independent lifetime.
	closed          bool
}

func openWinPTY(path, command, home string, env []string, cols, rows int) (windowsTerminal, error) {
	if !ValidSize(cols, rows) {
		return nil, errors.New("invalid terminal size")
	}
	api, dir, err := loadWinPTY()
	if err != nil {
		return nil, err
	}
	t := &winPTYTerminal{api: api, dir: dir}
	failed := true
	defer func() {
		if failed {
			t.Close()
		}
	}()
	var native uintptr
	cfg := api.call("winpty_config_new", 0, uintptr(unsafe.Pointer(&native)))
	if cfg == 0 {
		return nil, api.failure("configure", native)
	}
	defer api.call("winpty_config_free", cfg)
	api.call("winpty_config_set_initial_size", cfg, uintptr(cols), uintptr(rows))
	api.call("winpty_config_set_agent_timeout", cfg, 5000)
	t.console = api.call("winpty_open", cfg, uintptr(unsafe.Pointer(&native)))
	if t.console == 0 {
		return nil, api.failure("open", native)
	}
	helper := windows.Handle(api.call("winpty_agent_process", t.console))
	if err = windows.DuplicateHandle(windows.CurrentProcess(), helper, windows.CurrentProcess(), &t.helper, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return nil, err
	}
	if t.input, err = t.pipe("winpty_conin_name", windows.GENERIC_WRITE); err != nil {
		return nil, err
	}
	if t.output, err = t.pipe("winpty_conout_name", windows.GENERIC_READ); err != nil {
		return nil, err
	}
	application, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	line, err := windows.UTF16PtrFromString(command)
	if err != nil {
		return nil, err
	}
	directory, err := windows.UTF16PtrFromString(home)
	if err != nil {
		return nil, err
	}
	environment := append([]string(nil), env...)
	sort.Slice(environment, func(i, j int) bool { return strings.ToUpper(environment[i]) < strings.ToUpper(environment[j]) })
	block := utf16.Encode([]rune(strings.Join(environment, "\x00") + "\x00\x00"))
	// AUTO_SHUTDOWN | EXIT_AFTER_SHUTDOWN closes output and the hidden console
	// when the shell exits, including processes still attached to that console.
	spawn := api.call("winpty_spawn_config_new", 3, uintptr(unsafe.Pointer(application)), uintptr(unsafe.Pointer(line)),
		uintptr(unsafe.Pointer(directory)), uintptr(unsafe.Pointer(&block[0])), uintptr(unsafe.Pointer(&native)))
	runtime.KeepAlive(block)
	if spawn == 0 {
		return nil, api.failure("configure process", native)
	}
	defer api.call("winpty_spawn_config_free", spawn)
	var processError uint32
	if api.call("winpty_spawn", t.console, spawn, uintptr(unsafe.Pointer(&t.process)), 0, uintptr(unsafe.Pointer(&processError)), uintptr(unsafe.Pointer(&native))) == 0 {
		return nil, fmt.Errorf("%w (Windows error %d)", api.failure("start process", native), processError)
	}
	failed = false
	return t, nil
}

func (t *winPTYTerminal) pipe(name string, access uint32) (*os.File, error) {
	address := t.api.call(name, t.console)
	if address == 0 {
		return nil, errors.New("winpty pipe unavailable")
	}
	nameValue, err := winPTYString(address)
	if err != nil {
		return nil, err
	}
	path, err := windows.UTF16PtrFromString(nameValue)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(path, access, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), name), nil
}

func (t *winPTYTerminal) Read(p []byte) (int, error)  { return t.output.Read(p) }
func (t *winPTYTerminal) Write(p []byte) (int, error) { return t.input.Write(p) }
func (t *winPTYTerminal) Resize(cols, rows int) error {
	if !ValidSize(cols, rows) {
		return errors.New("invalid terminal size")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return os.ErrClosed
	}
	var native uintptr
	if t.api.call("winpty_set_size", t.console, uintptr(cols), uintptr(rows), uintptr(unsafe.Pointer(&native))) == 0 {
		return t.api.failure("resize", native)
	}
	return nil
}

func (t *winPTYTerminal) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.closed = true
	if t.input != nil {
		_ = t.input.Close()
	}
	if t.output != nil {
		_ = t.output.Close()
	}
	if t.console != 0 {
		t.api.call("winpty_free", t.console)
		t.console = 0
	}
	for _, handle := range []windows.Handle{t.helper, t.process} {
		if handle == 0 {
			continue
		}
		if state, err := windows.WaitForSingleObject(handle, 2000); err == nil && state == uint32(windows.WAIT_TIMEOUT) {
			_ = windows.TerminateProcess(handle, 1)
			_, _ = windows.WaitForSingleObject(handle, 1000)
		}
		_ = windows.CloseHandle(handle)
	}
	_ = t.api.dll.Release()
	removeWinPTYFiles(t.dir)
}
