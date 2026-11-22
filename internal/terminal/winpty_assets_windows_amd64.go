package terminal

import (
	"bytes"
	"compress/gzip"
	"embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"github.com/SECWG/UIR-Agent/internal/state"

	"golang.org/x/sys/windows"
)

// Pinned upstream binaries and MIT license are documented in winpty/README.md.
// They are private, per-terminal files; no PATH or working-directory DLL lookup.
//
//go:embed winpty/amd64/*.gz winpty/LICENSE
var winPTYFiles embed.FS

func winPTYAvailable() bool { return true }

type winPTYAPI struct {
	dll   *windows.DLL
	procs map[string]*windows.Proc
}

//go:uintptrescapes
func (a *winPTYAPI) call(name string, args ...uintptr) uintptr {
	result, _, _ := a.procs[name].Call(args...)
	return result
}

func (a *winPTYAPI) failure(operation string, native uintptr) error {
	if native == 0 {
		return fmt.Errorf("winpty %s failed", operation)
	}
	defer a.call("winpty_error_free", native)
	code := a.call("winpty_error_code", native)
	message := a.call("winpty_error_msg", native)
	text, _ := winPTYString(message)
	return fmt.Errorf("winpty %s: %s (%d)", operation, text, code)
}

var winPTYStringLength = windows.NewLazySystemDLL("kernel32.dll").NewProc("lstrlenW")

// Copy a DLL-owned UTF-16 string without treating its integer address as a Go
// pointer. The owning winpty object stays alive for the entire copy.
func winPTYString(address uintptr) (string, error) {
	if address == 0 {
		return "", fmt.Errorf("winpty string unavailable")
	}
	length, _, _ := winPTYStringLength.Call(address)
	if length > 32768 {
		return "", fmt.Errorf("winpty string too long")
	}
	buffer := make([]uint16, length+1)
	var read uintptr
	if err := windows.ReadProcessMemory(windows.CurrentProcess(), address, (*byte)(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)*2), &read); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buffer), nil
}

func loadWinPTY() (*winPTYAPI, string, error) {
	dir, err := os.MkdirTemp("", "uir-winpty-")
	if err != nil {
		return nil, "", err
	}
	failed := true
	defer func() {
		if failed {
			removeWinPTYFiles(dir)
		}
	}()
	if err = state.ProtectDirectory(dir); err != nil {
		return nil, "", err
	}
	for _, name := range []string{"winpty.dll", "winpty-agent.exe"} {
		compressed, readErr := winPTYFiles.ReadFile("winpty/amd64/" + name + ".gz")
		if readErr != nil {
			return nil, "", readErr
		}
		reader, readErr := gzip.NewReader(bytes.NewReader(compressed))
		if readErr != nil {
			return nil, "", readErr
		}
		contents, readErr := io.ReadAll(io.LimitReader(reader, 4*1024*1024+1))
		_ = reader.Close()
		if readErr != nil {
			return nil, "", readErr
		}
		if len(contents) > 4*1024*1024 {
			return nil, "", fmt.Errorf("winpty component too large")
		}
		file, writeErr := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if writeErr != nil {
			return nil, "", writeErr
		}
		_, writeErr = file.Write(contents)
		closeErr := file.Close()
		if writeErr != nil {
			return nil, "", writeErr
		}
		if closeErr != nil {
			return nil, "", closeErr
		}
	}
	handle, err := windows.LoadLibraryEx(filepath.Join(dir, "winpty.dll"), 0, windows.LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR|windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
	if err != nil {
		return nil, "", fmt.Errorf("load winpty: %w", err)
	}
	api := &winPTYAPI{dll: &windows.DLL{Name: filepath.Join(dir, "winpty.dll"), Handle: handle}, procs: map[string]*windows.Proc{}}
	for _, name := range []string{
		"winpty_config_new", "winpty_config_free", "winpty_config_set_initial_size", "winpty_config_set_agent_timeout",
		"winpty_open", "winpty_free", "winpty_agent_process", "winpty_conin_name", "winpty_conout_name",
		"winpty_spawn_config_new", "winpty_spawn_config_free", "winpty_spawn", "winpty_set_size",
		"winpty_error_free", "winpty_error_code", "winpty_error_msg",
	} {
		proc, findErr := api.dll.FindProc(name)
		if findErr != nil {
			_ = api.dll.Release()
			return nil, "", findErr
		}
		api.procs[name] = proc
	}
	failed = false
	return api, dir, nil
}

func removeWinPTYFiles(dir string) {
	// Only the two files we created and their empty directory are removed.
	for attempt := 0; attempt < 5; attempt++ {
		_ = os.Remove(filepath.Join(dir, "winpty.dll"))
		_ = os.Remove(filepath.Join(dir, "winpty-agent.exe"))
		if err := os.Remove(dir); err == nil || os.IsNotExist(err) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
