package execution

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/SECWG/UIR-Agent/internal/protocol"
)

type boundedWriter struct {
	file      *os.File
	size      int64
	truncated bool
	err       error
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	n := len(p)
	allowed := int64(n)
	if w.size+allowed > maxOutput {
		allowed = maxOutput - w.size
		w.truncated = true
	}
	if allowed > 0 {
		written, err := w.file.Write(p[:allowed])
		w.size += int64(written)
		if err != nil {
			w.err = err
			return written, err
		}
	}
	return n, nil
}

func (e *Engine) output(path string, w *boundedWriter, files map[string]string) protocol.Output {
	complete := !w.truncated && w.err == nil
	if w.size <= 32768 {
		b, err := os.ReadFile(path)
		if err != nil {
			b = nil
			complete = false
		}
		v := base64.StdEncoding.EncodeToString(b)
		o := protocol.Output{Complete: complete, Data: &v}
		if utf8.Valid(b) {
			o.Encoding = "utf-8"
		}
		return o
	}
	f, err := os.Open(path)
	if err != nil {
		v := ""
		return protocol.Output{Complete: false, Data: &v}
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		v := ""
		return protocol.Output{Complete: false, Data: &v}
	}
	ref := &protocol.File{ID: protocol.NewID(), Name: filepath.Base(path), Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}
	files[ref.ID] = path
	return protocol.Output{Complete: complete, File: ref}
}
