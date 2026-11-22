package execution

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/SECWG/UIR-Agent/internal/protocol"
	"github.com/SECWG/UIR-Agent/internal/state"
)

func command(run protocol.Run, work string) (*exec.Cmd, error) {
	c := run.Command
	if c.Kind == "exec" {
		return exec.Command(c.Program, c.Args...), nil
	}
	switch c.Shell {
	case "powershell", "pwsh":
		quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
		// Suppress module-autoload progress before New-Object initializes the encoding.
		content := "$ProgressPreference = 'SilentlyContinue'; [Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false); $OutputEncoding = [Console]::OutputEncoding; & {\n" +
			c.Content + "\n}"
		for _, a := range c.Args {
			content += " " + quote(a)
		}
		encoded := utf16.Encode([]rune(content))
		raw := make([]byte, len(encoded)*2)
		for i, v := range encoded {
			binary.LittleEndian.PutUint16(raw[i*2:], v)
		}
		return exec.Command(c.Shell, "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(raw)), nil
	case "cmd":
		if len(c.Args) > 0 {
			return nil, errors.New("cmd script arguments are not supported; use exec or PowerShell")
		}
		path := filepath.Join(work, "command.cmd")
		if err := os.WriteFile(path, []byte(c.Content), 0600); err != nil {
			return nil, err
		}
		return exec.Command("cmd", "/D", "/C", path), nil
	default:
		path := filepath.Join(work, "command.sh")
		if err := os.WriteFile(path, []byte(c.Content), 0600); err != nil {
			return nil, err
		}
		return exec.Command(c.Shell, append([]string{path}, c.Args...)...), nil
	}
}

func (e *Engine) execute(ctx context.Context, record *state.Record) {
	run := record.Run
	result := protocol.EmptyResult(run.ID, "failed", "EXECUTION_FAILED", "Command could not be started")
	result.Finished = nil
	files := map[string]string{}
	defer func() {
		if result.State == "unknown" {
			result.Finished, result.ExitCode = nil, nil
		}
		// Preparation and process-start failures also need their actual completion time.
		if result.Finished == nil && result.State != "unknown" {
			finished := protocol.Timestamp()
			result.Finished = &finished
		}
		if result.Error != nil && result.State == "completed" {
			result.State = "failed"
		}
		if result.State != "completed" && result.Error == nil {
			result.Error = &protocol.Fault{Code: strings.ToUpper(result.State), Message: "Task " + result.State}
		}
		if result.Error != nil {
			message := []rune(result.Error.Message)
			if len(message) > 2048 {
				result.Error.Message = string(message[:2048])
			}
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		record.Result = result
		record.Files = files
		record.Phase = "transferring"
		e.active = ""
		e.cancel = nil
		if e.save(record) == nil {
			e.emitResult(record)
			e.wakeFlush()
		}
	}()
	if ctx.Err() != nil {
		result = protocol.EmptyResult(run.ID, "canceled", "", " ")
		return
	}
	taskDir := filepath.Join(e.store.Dir, "tasks", run.ID)
	work := filepath.Join(taskDir, "work")
	if err := os.MkdirAll(work, 0700); err != nil {
		result.Error.Message = "Cannot create task workspace"
		return
	}
	if run.Command.Kind == "deploy" {
		result = e.deploy(ctx, record)
		return
	}
	if run.Command.Kind == "collect" {
		result = e.collect(ctx, record, taskDir, files)
		return
	}
	stdout, err := os.OpenFile(filepath.Join(taskDir, "stdout.bin"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer stdout.Close()
	stderr, err := os.OpenFile(filepath.Join(taskDir, "stderr.bin"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer stderr.Close()
	out := &boundedWriter{file: stdout}
	errOut := &boundedWriter{file: stderr}
	cmd, err := command(run, work)
	if err != nil {
		result.Error.Message = err.Error()
		return
	}
	cmd.Dir = work
	if run.Cwd != "" {
		if !filepath.IsAbs(run.Cwd) {
			result.Error.Message = "cwd must be an absolute endpoint directory"
			return
		}
		cmd.Dir = run.Cwd
	}
	cmd.Env = []string{}
	for _, v := range os.Environ() {
		k, _, _ := strings.Cut(v, "=")
		if !strings.HasPrefix(strings.ToUpper(k), "WG_IR_") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	for k, v := range run.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdout = out
	cmd.Stderr = errOut
	cmd.WaitDelay = 2 * time.Second
	e.mu.Lock()
	record.Phase = "running"
	err = e.save(record)
	e.mu.Unlock()
	if err != nil {
		return
	}
	started := protocol.Timestamp()
	result.Started = &started
	kill, closeProcess, err := StartProcess(cmd)
	if err != nil {
		result.Error = &protocol.Fault{Code: "START_FAILED", Message: err.Error()}
		return
	}
	defer closeProcess()
	e.emit(protocol.Status(run.ID, "running", ""))
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	err, stopErr, exited := waitProcess(ctx, done, kill, processStopTimeout)
	if !exited {
		result.State = "unknown"
		result.Error = &protocol.Fault{Code: "STOP_UNCONFIRMED", Message: stopErr.Error()}
		// Wait may still own the output writers. Do not snapshot mutable output or
		// report a completion time; deferred Close stops further file writes.
		result.Output.Stdout.Complete = false
		result.Output.Stderr.Complete = false
		return
	}
	stopErr = errors.Join(stopErr, kill()) // Terminate descendants which outlive the primary process.
	finished := protocol.Timestamp()
	result.Finished = &finished
	result.Error = nil
	result.State = "completed"
	if cmd.ProcessState != nil {
		code := cmd.ProcessState.ExitCode()
		result.ExitCode = &code
	}
	if ctx.Err() != nil {
		result.State = "canceled"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.State = "timed_out"
		}
	} else if err != nil {
		result.State = "failed"
		if result.ExitCode == nil || *result.ExitCode == 0 {
			result.Error = &protocol.Fault{Code: "OUTPUT_INCOMPLETE", Message: "Output pipes did not close cleanly"}
		}
	}
	if stopErr != nil {
		result.State = "unknown"
		result.Finished = nil
		result.Error = &protocol.Fault{Code: "STOP_UNCONFIRMED", Message: stopErr.Error()}
	}
	if err = stdout.Sync(); err != nil {
		out.err = err
	}
	if err = stderr.Sync(); err != nil {
		errOut.err = err
	}
	stdout.Close()
	stderr.Close()
	result.Output.Stdout = e.output(stdout.Name(), out, files)
	result.Output.Stderr = e.output(stderr.Name(), errOut, files)
}
