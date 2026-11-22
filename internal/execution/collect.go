package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/SECWG/UIR-Agent/internal/protocol"
	"github.com/SECWG/UIR-Agent/internal/state"
)

func capture(work, taskDir, name string) (protocol.File, string, error) {
	return captureContext(context.Background(), work, taskDir, name)
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func captureContext(ctx context.Context, work, taskDir, name string) (protocol.File, string, error) {
	return captureContextLimit(ctx, work, taskDir, name, maxFile)
}

func captureContextLimit(ctx context.Context, work, taskDir, name string, remaining int64) (protocol.File, string, error) {
	var ref protocol.File
	if !filepath.IsLocal(name) || strings.ContainsAny(name, ":\x00") {
		return ref, "", errors.New("invalid output path")
	}
	root, err := os.OpenRoot(work)
	if err != nil {
		return ref, "", err
	}
	defer root.Close()
	info, err := root.Lstat(name)
	if err != nil {
		return ref, "", err
	}
	if !info.Mode().IsRegular() {
		return ref, "", errors.New("REGULAR_FILE_REQUIRED")
	}
	file, err := openCapture(root, name)
	if err != nil {
		return ref, "", err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return ref, "", err
	}
	if !before.Mode().IsRegular() {
		return ref, "", errors.New("REGULAR_FILE_REQUIRED")
	}
	if before.Size() > maxFile {
		return ref, "", errors.New("COLLECTION_FILE_TOO_LARGE")
	}
	if before.Size() > remaining {
		return ref, "", errors.New("COLLECTION_TOTAL_TOO_LARGE")
	}
	ref = protocol.File{ID: protocol.NewID(), Name: filepath.Base(name)}
	target := filepath.Join(taskDir, ref.ID+".bin")
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return ref, "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), contextReader{ctx, io.LimitReader(file, maxFile+1)})
	syncErr := out.Sync()
	out.Close()
	after, statErr := file.Stat()
	if err != nil || syncErr != nil || statErr != nil {
		os.Remove(target)
		return ref, "", errors.Join(err, syncErr, statErr)
	}
	if n > maxFile || n != before.Size() || after.Size() != before.Size() || !before.ModTime().Equal(after.ModTime()) {
		os.Remove(target)
		return ref, "", errors.New("FILE_CHANGED_DURING_READ")
	}
	ref.Size = n
	ref.SHA256 = hex.EncodeToString(h.Sum(nil))
	return ref, target, nil
}

// Collection is an explicit read operation. Shell output never becomes a collected file.
func (e *Engine) collect(ctx context.Context, record *state.Record, taskDir string, files map[string]string) *protocol.Result {
	result := protocol.EmptyResult(record.Run.ID, "completed", "", "")
	result.Error = nil
	started := protocol.Timestamp()
	result.Started = &started
	e.mu.Lock()
	record.Phase = "running"
	err := e.save(record)
	e.mu.Unlock()
	if err != nil {
		result.State = "failed"
		result.Error = &protocol.Fault{Code: "PERSIST_FAILED", Message: "Could not persist collection state"}
		return result
	}
	e.emit(protocol.Status(record.Run.ID, "running", ""))
	private, err := filepath.EvalSymlinks(e.store.Dir)
	if err != nil {
		result.State = "failed"
		result.Error = &protocol.Fault{Code: "PATH_UNAVAILABLE", Message: "Cannot resolve private agent directory"}
		return result
	}
	var total int64
	for _, source := range record.Run.Command.Paths {
		if ctx.Err() != nil {
			break
		}
		captureOne := func() (protocol.File, string, error) {
			parent, err := filepath.EvalSymlinks(filepath.Dir(source))
			if err != nil {
				return protocol.File{}, "", err
			}
			resolved := filepath.Join(parent, filepath.Base(source))
			rel, err := filepath.Rel(private, resolved)
			if err == nil && (rel == "." || !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." && !filepath.IsAbs(rel)) {
				return protocol.File{}, "", errors.New("agent private storage cannot be collected")
			}
			size, err := e.store.CacheBytes()
			if err != nil || size > maxCache-maxFile {
				return protocol.File{}, "", errors.New("COLLECTION_CACHE_FULL")
			}
			return captureContextLimit(ctx, parent, taskDir, filepath.Base(source), maxFile-total)
		}
		file, path, err := captureOne()
		if err != nil {
			message := []rune(collectionError(err))
			if len(message) > 2048 {
				message = message[:2048]
			}
			result.FileErrors = append(result.FileErrors, protocol.FileError{Path: source, Message: string(message)})
			continue
		}
		file.SourcePath = source
		total += file.Size
		result.Files = append(result.Files, file)
		files[file.ID] = path
	}
	finished := protocol.Timestamp()
	result.Finished = &finished
	if ctx.Err() != nil {
		result.State = "canceled"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.State = "timed_out"
		}
		result.Error = &protocol.Fault{Code: strings.ToUpper(result.State), Message: "File collection interrupted"}
	} else if len(result.FileErrors) > 0 {
		result.State = "failed"
		result.Error = &protocol.Fault{Code: "COLLECTION_INCOMPLETE", Message: "Some requested files could not be collected; successful files are retained"}
	}
	return result
}

func collectionError(err error) string {
	switch {
	case os.IsNotExist(err):
		return "FILE_NOT_FOUND"
	case os.IsPermission(err):
		return "FILE_ACCESS_DENIED"
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		return "ENDPOINT_STORAGE_FULL"
	default:
		return err.Error()
	}
}
