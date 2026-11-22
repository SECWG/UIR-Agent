package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

var uuidPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

// ValidID validates a protocol UUID without exposing mutable validator state.
func ValidID(value string) bool { return uuidPattern.MatchString(value) }

// Command is decoded from command_b64 before validation and execution.
type Command struct {
	ToolID    string   `json:"tool_id,omitempty"`
	Filename  string   `json:"filename,omitempty"`
	Size      int64    `json:"size_bytes,omitempty"`
	SHA256    string   `json:"sha256,omitempty"`
	Directory string   `json:"directory,omitempty"`
	Kind      string   `json:"kind"`
	Program   string   `json:"program,omitempty"`
	Shell     string   `json:"shell,omitempty"`
	Content   string   `json:"content,omitempty"`
	Args      []string `json:"args,omitempty"`
	Paths     []string `json:"paths,omitempty"`
}

// Run is the normalized execution request; command.go owns its wire encoding.
type Run struct {
	Type    string            `json:"type"`
	ID      string            `json:"task_id"`
	Command Command           `json:"command"`
	Timeout int64             `json:"timeout_ms"`
	Expires string            `json:"expires_at"`
	Cwd     string            `json:"cwd,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

type File struct {
	ID         string `json:"file_id"`
	Name       string `json:"name"`
	Size       int64  `json:"size_bytes"`
	SHA256     string `json:"sha256"`
	SourcePath string `json:"source_path,omitempty"`
}

type Output struct {
	Complete bool    `json:"complete"`
	Data     *string `json:"data_b64,omitempty"`
	File     *File   `json:"file,omitempty"`
	Encoding string  `json:"encoding,omitempty"`
}

type Result struct {
	Type     string  `json:"type"`
	ID       string  `json:"task_id"`
	State    string  `json:"state"`
	ExitCode *int    `json:"exit_code"`
	Started  *string `json:"started_at"`
	Finished *string `json:"finished_at"`
	Output   struct {
		Stdout Output `json:"stdout"`
		Stderr Output `json:"stderr"`
	} `json:"output"`
	Files      []File      `json:"files,omitempty"`
	FileErrors []FileError `json:"file_errors,omitempty"`
	Error      *Fault      `json:"error,omitempty"`
}

type FileError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

type Fault struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Transfer reports file progress independently of the immutable task result.
type Transfer struct {
	FileID  string `json:"file_id"`
	State   string `json:"state"`
	Sent    int64  `json:"sent_bytes"`
	Error   *Fault `json:"error,omitempty"`
	RetryAt string `json:"retry_at,omitempty"`
}

func Status(id, phase, code string) any {
	v := map[string]any{"type": "status", "task_id": id}
	if phase != "" {
		v["phase"] = phase
	}
	if code != "" {
		v["error"] = &Fault{code, code}
	}
	return v
}

func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	s := hex.EncodeToString(b)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

func Timestamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func EmptyResult(id, state, code, message string) *Result {
	now := Timestamp()
	empty := ""
	r := &Result{Type: "result", ID: id, State: state, Finished: &now}
	r.Output.Stdout = Output{Complete: true, Data: &empty}
	r.Output.Stderr = Output{Complete: true, Data: &empty}
	if code != "" {
		r.Error = &Fault{code, message}
	}
	if state != "completed" && r.Error == nil {
		r.Error = &Fault{strings.ToUpper(state), "Task " + state}
	}
	if state == "unknown" {
		r.Finished = nil
	}
	return r
}

func Decode[T any](raw []byte) (T, error) {
	var v T
	err := json.Unmarshal(raw, &v)
	return v, err
}
