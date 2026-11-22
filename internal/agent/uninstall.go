package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/SECWG/UIR-Agent/internal/diagnostics"
	"github.com/SECWG/UIR-Agent/internal/execution"
	"github.com/SECWG/UIR-Agent/internal/protocol"
	"github.com/SECWG/UIR-Agent/internal/state"
)

type uninstallRequest struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Token string `json:"receipt_token"`
}

type uninstallPlan struct {
	ID         string `json:"id"`
	Token      string `json:"token"`
	Server     string `json:"server"`
	Executable string `json:"executable"`
	SHA256     string `json:"sha256"`
	DataDir    string `json:"dataDir"`
	Proxy      string `json:"proxy"`
	PID        int    `json:"pid"`
}

type uninstallResult struct {
	Status    string   `json:"status"`
	Residuals []string `json:"residuals"`
}

// UninstallIntent 在 Serve 的文件、终端和存储句柄全部关闭后交给入口完成清理。
type UninstallIntent struct {
	plan   uninstallPlan
	finish func(uninstallResult) error
	abort  func()
}

func (*UninstallIntent) Error() string { return "client uninstall requested" }

func prepareUninstall(e *execution.Engine, raw []byte, o Options, id state.Identity) (*UninstallIntent, error) {
	var request uninstallRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || request.Type != "uninstall_request" ||
		!protocol.ValidID(request.ID) || !regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`).MatchString(request.Token) {
		return nil, errors.New("UNINSTALL_REQUEST_INVALID")
	}
	var intent *UninstallIntent
	err := e.PrepareUninstall(func(dir string) error {
		var err error
		intent, err = prepareUninstallIntent(request, o, id, dir)
		return err
	})
	return intent, err
}

func prepareUninstallIntent(request uninstallRequest, o Options, id state.Identity, dir string) (*UninstallIntent, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, errors.New("UNINSTALL_PATH_UNAVAILABLE")
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, errors.New("UNINSTALL_PATH_UNAVAILABLE")
	}
	dataDir, err := filepath.EvalSymlinks(dir)
	if err != nil || filepath.Dir(dataDir) == dataDir {
		return nil, errors.New("UNINSTALL_PATH_UNAVAILABLE")
	}
	file, err := os.Open(executable)
	if err != nil {
		return nil, errors.New("UNINSTALL_PATH_UNAVAILABLE")
	}
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	file.Close()
	if err != nil {
		return nil, errors.New("UNINSTALL_PATH_UNAVAILABLE")
	}
	plan := uninstallPlan{ID: request.ID, Token: request.Token, Server: id.Server, Executable: executable,
		SHA256: hex.EncodeToString(hash.Sum(nil)), DataDir: dataDir, Proxy: o.Proxy, PID: os.Getpid()}
	finish, abort, err := prepareUninstallCleanup(plan)
	if err != nil {
		return nil, err
	}
	return &UninstallIntent{plan: plan, finish: finish, abort: abort}, nil
}

// 只移除专属数据库和任务缓存；目录内其他文件及程序所在目录均不递归删除。
func removeAgentData(dir string) []string {
	residuals := []string{}
	if !filepath.IsAbs(dir) || filepath.Dir(filepath.Clean(dir)) == filepath.Clean(dir) {
		return []string{dir}
	}
	if info, err := os.Lstat(dir); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return []string{dir}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			residuals = append(residuals, dir)
		}
		return residuals
	}
	if err := root.RemoveAll("tasks"); err != nil {
		residuals = append(residuals, filepath.Join(dir, "tasks"))
	}
	ownedFiles := append([]string{"state.db", "identity.json", "identity.lock", "recovery.pending.json"}, diagnostics.OwnedFiles()...)
	for _, name := range ownedFiles {
		if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			residuals = append(residuals, filepath.Join(dir, name))
		}
	}
	root.Close()
	_ = os.Remove(dir) // 只移除空目录，保留用户后来放入的其他文件。
	return residuals
}

func FinishUninstall(intent *UninstallIntent) error {
	result := uninstallResult{Status: "completed", Residuals: removeAgentData(intent.plan.DataDir)}
	if len(result.Residuals) != 0 {
		result.Status = "partial"
	}
	return intent.finish(result)
}

func reportUninstall(plan uninstallPlan, result uninstallResult) error {
	client, _, err := Client(Options{Server: plan.Server, Proxy: plan.Proxy})
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	data, _ := json.Marshal(result)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for attempt := 0; attempt < 3; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, "POST", plan.Server+"/v1/agent/uninstall/"+plan.ID+"/complete", bytes.NewReader(data))
		req.Header.Set("Authorization", "Bearer "+plan.Token)
		req.Header.Set("Content-Type", "application/json")
		res, sendErr := client.Do(req)
		if sendErr == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return nil
			}
			if res.StatusCode < 500 {
				return errors.New("UNINSTALL_RECEIPT_REJECTED")
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errors.New("UNINSTALL_RECEIPT_UNCONFIRMED")
}
