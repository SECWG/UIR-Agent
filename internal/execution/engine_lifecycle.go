package execution

import (
	"context"
	"errors"
	"io"
)

// DataDir is immutable for the lifetime of an engine and its exclusive store.
func (e *Engine) DataDir() string { return e.store.Dir }

// Err reports cancellation, including a persistence failure that stops execution.
func (e *Engine) Err() error { return e.ctx.Err() }

// Context lets control connections observe engine cancellation without owning it.
func (e *Engine) Context() context.Context { return e.ctx }

// ManagementReadSlots retains blocked local reads across control reconnects.
func (e *Engine) ManagementReadSlots() chan struct{} { return e.managementSlots }

// SetToolDownloader is configured during startup before accepting any task.
func (e *Engine) SetToolDownloader(download func(context.Context, string) (io.ReadCloser, error)) {
	e.downloadTool = download
}

// PrepareUninstall keeps admission locked while the caller prepares its cleanup
// helper. A failed preparation leaves the running engine unchanged.
func (e *Engine) PrepareUninstall(prepare func(string) error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.uninstalling || e.active != "" || len(e.transferring) != 0 || len(e.managementSlots) != 0 {
		return errors.New("AGENT_TASKS_PENDING")
	}
	for _, record := range e.records {
		if record.Result == nil || !record.Ack || record.Unresolved() {
			return errors.New("AGENT_TASKS_PENDING")
		}
		for fileID := range record.Files {
			if !record.Uploaded[fileID] && !record.Abandoned {
				return errors.New("AGENT_TASKS_PENDING")
			}
		}
	}
	if err := prepare(e.store.Dir); err != nil {
		return err
	}
	e.uninstalling = true
	return nil
}

// CancelUninstall releases the reservation if its acceptance could not be sent.
func (e *Engine) CancelUninstall() {
	e.mu.Lock()
	e.uninstalling = false
	e.mu.Unlock()
}
