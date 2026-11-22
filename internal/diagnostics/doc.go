// Package diagnostics manages bounded runtime logs and crash reports. The command
// entry point opens these only for an admitted background instance and closes them
// before normal exit or uninstall. Panic unwinding preserves the runtime's crash
// output handle until its final report has been written.
package diagnostics
