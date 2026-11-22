package agent

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/SECWG/UIR-Agent/internal/state"
)

//go:embed uninstall-windows.ps1
var uninstallWindowsScript string

func uninstallHelper() (string, error) {
	systemRoot := os.Getenv("SystemRoot")
	powershell := filepath.Join(systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	for _, path := range []string{powershell} {
		if !filepath.IsAbs(path) {
			return "", errors.New("UNINSTALL_HELPER_UNAVAILABLE")
		}
		if stat, err := os.Stat(path); err != nil || !stat.Mode().IsRegular() {
			return "", errors.New("UNINSTALL_HELPER_UNAVAILABLE")
		}
	}
	return powershell, nil
}

func uninstallAvailable() bool {
	_, err := uninstallHelper()
	return err == nil
}

func prepareUninstallCleanup(plan uninstallPlan) (func(uninstallResult) error, func(), error) {
	powershell, err := uninstallHelper()
	if err != nil {
		return nil, nil, err
	}
	if plan.Proxy == "" {
		req, _ := http.NewRequest("POST", plan.Server, nil)
		if proxy, err := http.ProxyFromEnvironment(req); err == nil && proxy != nil {
			plan.Proxy = proxy.String()
		}
	}
	dir, err := os.MkdirTemp("", "wg-ir-uninstall-")
	if err != nil {
		return nil, nil, errors.New("UNINSTALL_HELPER_UNAVAILABLE")
	}
	script := filepath.Join(dir, "cleanup.ps1")
	cleanup := func() {
		_ = os.Remove(script)
		_ = os.Remove(dir)
	}
	if state.ProtectDirectory(dir) != nil || os.WriteFile(script, []byte("\xef\xbb\xbf"+uninstallWindowsScript), 0600) != nil {
		cleanup()
		return nil, nil, errors.New("UNINSTALL_HELPER_UNAVAILABLE")
	}
	cmd := exec.Command(powershell, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Dir = os.TempDir()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cleanup()
		return nil, nil, errors.New("UNINSTALL_HELPER_UNAVAILABLE")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		cleanup()
		return nil, nil, errors.New("UNINSTALL_HELPER_UNAVAILABLE")
	}
	if err = cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		cleanup()
		return nil, nil, errors.New("UNINSTALL_HELPER_UNAVAILABLE")
	}
	abort := func() {
		stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		stdout.Close()
		cleanup()
	}
	// 一次性回执凭据只通过管道传入，不写入文件或命令行。
	if json.NewEncoder(stdin).Encode(struct {
		uninstallPlan
		UserAgent string `json:"userAgent"`
	}{plan, userAgent()}) != nil {
		abort()
		return nil, nil, errors.New("UNINSTALL_HELPER_UNAVAILABLE")
	}
	ready := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		ready <- scanner.Scan() && scanner.Text() == "READY"
	}()
	select {
	case ok := <-ready:
		if !ok {
			abort()
			return nil, nil, errors.New("UNINSTALL_HELPER_UNAVAILABLE")
		}
	case <-time.After(6 * time.Second):
		abort()
		return nil, nil, errors.New("UNINSTALL_HELPER_UNAVAILABLE")
	}
	return func(result uninstallResult) error {
		err := json.NewEncoder(stdin).Encode(result)
		stdin.Close()
		stdout.Close()
		if err != nil {
			abort()
			return errors.New("UNINSTALL_HELPER_UNAVAILABLE")
		}
		// 清理进程已持有本进程句柄，等待入口退出后删除当前 EXE 并回传结果。
		return cmd.Process.Release()
	}, abort, nil
}
