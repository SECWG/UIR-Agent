package main

import (
	"errors"
	"runtime/debug"

	"github.com/SECWG/UIR-Agent/internal/diagnostics"
)

type backgroundLogs struct{ output *diagnostics.RuntimeLog }

func openBackgroundLogs(dir string) (*backgroundLogs, error) {
	output, err := diagnostics.OpenRuntimeLog(dir)
	if err != nil {
		return nil, err
	}
	crash, err := diagnostics.OpenCrashLog(dir)
	if err != nil {
		output.Close()
		return nil, err
	}
	// SetCrashOutput duplicates the handle. It does not depend on stderr, a
	// forwarding goroutine, or the lifetime of the launcher that already exited.
	err = debug.SetCrashOutput(crash, debug.CrashOptions{})
	crash.Close()
	if err != nil {
		output.Close()
		return nil, err
	}
	return &backgroundLogs{output: output}, nil
}

// Close is for normal return only. Disabling crash output from a panic-unwind
// defer would lose the stack trace, which the runtime prints after defers run.
// Release the duplicate handle before uninstall removes files on Windows.
func (l *backgroundLogs) Close() error {
	return errors.Join(debug.SetCrashOutput(nil, debug.CrashOptions{}), l.output.Close())
}
