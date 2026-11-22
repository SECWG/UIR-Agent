package execution

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/SECWG/UIR-Agent/internal/protocol"
	"github.com/SECWG/UIR-Agent/internal/state"
)

const maxTool = 500000000

func validDeploy(c protocol.Command) error {
	if !protocol.ValidID(c.ToolID) || len(c.SHA256) != 64 || c.Size < 1 || c.Size > maxTool {
		return errors.New("invalid tool file")
	}
	if _, err := hex.DecodeString(c.SHA256); err != nil {
		return errors.New("invalid tool hash")
	}
	if !filepath.IsLocal(c.Filename) ||
		strings.ContainsAny(c.Filename, "\\/:\x00<>\"|?*\r\n") ||
		strings.HasSuffix(c.Filename, ".") ||
		strings.HasSuffix(c.Filename, " ") {
		return errors.New("invalid tool filename")
	}
	if c.Directory != "" &&
		(!filepath.IsAbs(c.Directory) ||
			strings.ContainsAny(c.Directory, "\x00\r\n") ||
			strings.HasPrefix(c.Directory, `\\`) ||
			strings.HasPrefix(c.Directory, "//")) {
		return errors.New("tool directory must be an absolute local path")
	}
	return nil
}

// A deployment retains the file for later commands. It never executes or unpacks it.
func (e *Engine) deploy(ctx context.Context, record *state.Record) *protocol.Result {
	result := protocol.EmptyResult(record.Run.ID, "failed", "TOOL_TRANSFER_FAILED", "Tool file was not delivered")
	started := protocol.Timestamp()
	result.Started = &started
	fail := func(err error) *protocol.Result {
		result.Error.Message = err.Error()
		finished := protocol.Timestamp()
		result.Finished = &finished
		if ctx.Err() != nil {
			result.State = "canceled"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				result.State = "timed_out"
			}
		}
		return result
	}
	if e.downloadTool == nil {
		return fail(errors.New("tool download unavailable"))
	}
	command := record.Run.Command
	directory := command.Directory
	if directory == "" {
		directory = filepath.Join(os.TempDir(), "wg-ir-tool-"+record.Run.ID)
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fail(err)
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return fail(err)
	}
	private, err := filepath.EvalSymlinks(e.store.Dir)
	if err != nil {
		return fail(err)
	}
	if rel, err := filepath.Rel(private, resolved); err == nil && (rel == "." || filepath.IsLocal(rel)) {
		return fail(errors.New("tool directory cannot be inside agent private storage"))
	}
	if free, err := state.AvailableBytes(resolved); err != nil || free < uint64(command.Size)+(256<<20) {
		return fail(errors.New("insufficient or unknown disk space"))
	}
	root, err := os.OpenRoot(resolved)
	if err != nil {
		return fail(err)
	}
	defer root.Close()
	file, err := root.OpenFile(command.Filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fail(errors.New("cannot create tool file; existing files are not overwritten"))
	}
	complete := false
	defer func() {
		file.Close()
		if !complete {
			_ = root.Remove(command.Filename)
		}
	}()
	e.mu.Lock()
	record.Phase = "running"
	err = e.save(record)
	e.mu.Unlock()
	if err != nil {
		return fail(err)
	}
	e.emit(protocol.Status(record.Run.ID, "running", ""))
	body, err := e.downloadTool(ctx, record.Run.ID)
	if err != nil {
		return fail(err)
	}
	defer body.Close()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, hash), contextReader{ctx, io.LimitReader(body, command.Size+1)})
	if err != nil {
		return fail(err)
	}
	if size != command.Size || hex.EncodeToString(hash.Sum(nil)) != command.SHA256 {
		return fail(errors.New("tool file size or SHA-256 mismatch"))
	}
	if err = ctx.Err(); err != nil {
		return fail(err)
	}
	if err = file.Sync(); err != nil {
		return fail(err)
	}
	if err = file.Close(); err != nil {
		return fail(err)
	}
	complete = true
	receipt, _ := json.Marshal(map[string]any{
		"path":      filepath.Join(resolved, command.Filename),
		"sizeBytes": size,
		"sha256":    command.SHA256,
	})
	encoded := base64.StdEncoding.EncodeToString(receipt)
	result.State = "completed"
	result.Error = nil
	finished := protocol.Timestamp()
	result.Finished = &finished
	result.Output.Stdout = protocol.Output{Complete: true, Data: &encoded, Encoding: "utf-8"}
	return result
}
