package execution

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/SECWG/UIR-Agent/internal/protocol"
	"github.com/SECWG/UIR-Agent/internal/state"
)

const maxTransferAttempts = 6
const transferIdleTimeout = 90 * time.Second
const transferTotalTimeout = 2 * time.Hour

var transferCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

type transferFailure struct {
	fault protocol.Fault
	retry bool
}

func (f *transferFailure) Error() string { return f.fault.Message }

func classifyTransferError(err error) (protocol.Fault, bool) {
	var failure *transferFailure
	if errors.As(err, &failure) {
		return failure.fault, failure.retry
	}
	var cert x509.UnknownAuthorityError
	var host x509.HostnameError
	var invalid x509.CertificateInvalidError
	if errors.As(err, &cert) || errors.As(err, &host) || errors.As(err, &invalid) {
		return protocol.Fault{Code: "TRANSFER_TLS_ERROR", Message: "平台证书校验失败，请检查地址与 CA 配置。"}, false
	}
	return protocol.Fault{Code: "TRANSFER_NETWORK_ERROR", Message: "网络中断或回传超时。"}, true
}

func (e *Engine) emitTransfer(taskID string, state protocol.Transfer) {
	e.emit(map[string]any{"type": "status", "task_id": taskID, "phase": "transferring", "transfer": state})
}

func (e *Engine) transferCheckpoint(taskID, fileID string, value state.TransferCheckpoint) error {
	e.mu.Lock()
	r := e.records[taskID]
	if r.Transfers == nil {
		r.Transfers = map[string]state.TransferCheckpoint{}
	}
	r.Transfers[fileID] = value
	err := e.save(r)
	e.mu.Unlock()
	if err == nil {
		e.emitTransfer(taskID, value.Transfer)
	}
	return err
}

func resultFile(result *protocol.Result, id string) protocol.File {
	for _, file := range result.Files {
		if file.ID == id {
			return file
		}
	}
	for _, output := range []protocol.Output{result.Output.Stdout, result.Output.Stderr} {
		if output.File != nil && output.File.ID == id {
			return *output.File
		}
	}
	return protocol.File{ID: id}
}

// Retrying uploads always uses the saved snapshot; it never re-executes a command.
func (e *Engine) transferFile(ctx context.Context, client *http.Client, identity state.Identity, r *state.Record, fileID, path string) (bool, error) {
	e.mu.Lock()
	checkpoint := e.records[r.Run.ID].Transfers[fileID]
	e.mu.Unlock()
	if checkpoint.State == "failed" {
		return false, nil
	}
	if next, err := time.Parse(time.RFC3339Nano, checkpoint.RetryAt); err == nil && time.Now().Before(next) {
		return false, nil
	}
	state := protocol.Transfer{FileID: fileID, State: "uploading"}
	e.emitTransfer(r.Run.ID, state)
	err := upload(ctx, client, identity, r.Run.ID, resultFile(r.Result, fileID), path, func(sent int64) {
		e.emitTransfer(r.Run.ID, protocol.Transfer{FileID: fileID, State: "uploading", Sent: sent})
	})
	if err == nil {
		return true, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	fault, retry := classifyTransferError(err)
	checkpoint.Attempts++
	state.Error = &fault
	if retry && checkpoint.Attempts < maxTransferAttempts {
		state.State = "retrying"
		delay := min(30*time.Second*time.Duration(1<<min(checkpoint.Attempts-1, 4)), 5*time.Minute)
		state.RetryAt = time.Now().Add(delay).UTC().Format(time.RFC3339Nano)
	} else {
		state.State = "failed"
		if retry {
			state.Error = &protocol.Fault{Code: "TRANSFER_RETRIES_EXHAUSTED", Message: fault.Message + " 连续 6 次未成功，已停止自动重试。"}
		}
	}
	checkpoint.Transfer = state
	return false, e.transferCheckpoint(r.Run.ID, fileID, checkpoint)
}

type progressReader struct {
	reader   io.Reader
	sent     int64
	last     time.Time
	progress func(int64)
	touch    func()
}

func (r *progressReader) Read(bytes []byte) (int, error) {
	n, err := r.reader.Read(bytes)
	r.sent += int64(n)
	if n > 0 && r.touch != nil {
		r.touch()
	}
	if time.Since(r.last) >= 2*time.Second || err == io.EOF {
		r.last = time.Now()
		r.progress(r.sent)
	}
	return n, err
}

func upload(ctx context.Context, client *http.Client, id state.Identity, taskID string, file protocol.File, path string, progress func(int64)) error {
	ctx, touch, finish := transferContext(ctx, transferIdleTimeout, transferTotalTimeout)
	defer finish()
	f, err := os.Open(path)
	if err != nil {
		return &transferFailure{protocol.Fault{Code: "TRANSFER_SOURCE_UNAVAILABLE", Message: "终端缓存文件不存在或无法读取。"}, false}
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() != file.Size {
		return &transferFailure{protocol.Fault{Code: "TRANSFER_SOURCE_CHANGED", Message: "终端缓存文件大小已变化。"}, false}
	}
	reader := &progressReader{reader: io.LimitReader(f, file.Size), progress: progress, last: time.Now(), touch: touch}
	req, err := http.NewRequestWithContext(ctx, "PUT", id.Server+"/v1/agent/tasks/"+taskID+"/files/"+file.ID, reader)
	if err != nil {
		return &transferFailure{protocol.Fault{Code: "TRANSFER_ADDRESS_INVALID", Message: "平台回传地址无效。"}, false}
	}
	req.ContentLength = info.Size()
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Authorization", "Bearer "+id.Credential)
	req.Header.Set("X-Device-Id", id.DeviceID)
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 4096))
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		var body struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(b, &body)
		code := body.Code
		if !transferCodePattern.MatchString(code) {
			code = fmt.Sprintf("TRANSFER_HTTP_%d", res.StatusCode)
		}
		retry := res.StatusCode == 408 || res.StatusCode == 429 || res.StatusCode >= 500
		return &transferFailure{protocol.Fault{Code: code, Message: fmt.Sprintf("平台拒绝文件回传（HTTP %d）。", res.StatusCode)}, retry}
	}
	var receipt struct {
		Availability string `json:"availability"`
		Size         int64  `json:"sizeBytes"`
		SHA256       string `json:"sha256"`
	}
	if json.Unmarshal(b, &receipt) != nil ||
		receipt.Availability != "available" ||
		receipt.Size != file.Size ||
		receipt.SHA256 != file.SHA256 {
		return &transferFailure{protocol.Fault{Code: "TRANSFER_ACK_INVALID", Message: "平台未返回有效的文件校验确认。"}, true}
	}
	return nil
}

// A moving transfer may outlive ten minutes. A stalled upload or receipt still times out.
func transferContext(parent context.Context, idle, total time.Duration) (context.Context, func(), func()) {
	ctx, cancel := context.WithTimeout(parent, total)
	timer := time.AfterFunc(idle, cancel)
	return ctx, func() { timer.Reset(idle) }, func() {
		timer.Stop()
		cancel()
	}
}
