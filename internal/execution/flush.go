package execution

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/SECWG/UIR-Agent/internal/protocol"
	"github.com/SECWG/UIR-Agent/internal/state"
)

const resultWindow = 4

const resultRetryDelay = 30 * time.Second

const transferConcurrency = 2

func (e *Engine) flush(ctx context.Context, client *http.Client, id state.Identity) {
	e.flushMu.Lock()
	defer e.flushMu.Unlock()
	if ctx.Err() != nil || e.ctx.Err() != nil {
		return
	}
	e.mu.Lock()
	var pending []*state.Record
	for _, r := range e.records {
		if r.Result == nil || r.Cleaned {
			continue
		}
		b, _ := json.Marshal(r)
		copy, _ := protocol.Decode[state.Record](b)
		pending = append(pending, &copy)
	}
	e.mu.Unlock()
	sort.Slice(pending, func(i, j int) bool {
		var a, b string
		if pending[i].Result.Finished != nil {
			a = *pending[i].Result.Finished
		}
		if pending[j].Result.Finished != nil {
			b = *pending[j].Result.Finished
		}
		if a != b {
			return a < b
		}
		return pending[i].Run.ID < pending[j].Run.ID
	})
	// Control recovery never waits for an HTTP transfer.
	for _, r := range pending {
		if ctx.Err() != nil {
			return
		}
		if !r.Ack && e.reserveResult(r.Run.ID, time.Now()) {
			e.emit(r.Result)
		}
	}
	for _, r := range pending {
		if ctx.Err() != nil {
			return
		}
		if !r.Ack {
			continue
		}
		all := true
		fileIDs := make([]string, 0, len(r.Files))
		for fileID := range r.Files {
			fileIDs = append(fileIDs, fileID)
		}
		sort.Slice(fileIDs, func(i, j int) bool {
			a, b := resultFile(r.Result, fileIDs[i]), resultFile(r.Result, fileIDs[j])
			if a.Size == b.Size {
				return a.ID < b.ID
			}
			return a.Size < b.Size
		})
		for _, fileID := range fileIDs {
			if r.Uploaded[fileID] || r.Abandoned {
				e.mu.Lock()
				if e.transferring[r.Run.ID+"/"+fileID] {
					all = false
				}
				e.mu.Unlock()
				continue
			}
			all = false
			key := r.Run.ID + "/" + fileID
			e.mu.Lock()
			checkpoint := e.records[r.Run.ID].Transfers[fileID]
			due, _ := time.Parse(time.RFC3339Nano, checkpoint.RetryAt)
			start := !e.records[r.Run.ID].Abandoned &&
				!e.records[r.Run.ID].Uploaded[fileID] &&
				!e.transferring[key] &&
				len(e.transferring) < transferConcurrency &&
				checkpoint.State != "failed" &&
				!due.After(time.Now())
			if start {
				e.transferring[key] = true
				e.transferWG.Add(1)
			}
			e.mu.Unlock()
			if start {
				go e.runTransfer(ctx, client, id, r, fileID, r.Files[fileID])
			}
		}
		if all && !r.Unresolved() {
			e.mu.Lock()
			due := e.cleanupRetry[r.Run.ID]
			e.mu.Unlock()
			if due.After(time.Now()) {
				continue
			}
			// Task IDs are validated UUIDs, and cleanup is confined to this private root.
			root := filepath.Join(e.store.Dir, "tasks")
			target := filepath.Join(root, r.Run.ID)
			if protocol.ValidID(r.Run.ID) && filepath.Dir(target) == root {
				if err := os.RemoveAll(target); err == nil {
					e.mu.Lock()
					current := e.records[r.Run.ID]
					current.Cleaned = true
					err = e.save(current)
					if err == nil {
						delete(e.records, r.Run.ID)
						delete(e.cleanupRetry, r.Run.ID)
					}
					e.mu.Unlock()
					if err != nil {
						return
					}
				} else {
					e.mu.Lock()
					e.cleanupRetry[r.Run.ID] = time.Now().Add(30 * time.Second)
					e.mu.Unlock()
				}
			}
		}
	}
}

func (e *Engine) runTransfer(ctx context.Context, client *http.Client, id state.Identity, r *state.Record, fileID, path string) {
	defer e.transferWG.Done()
	defer func() {
		e.mu.Lock()
		delete(e.transferring, r.Run.ID+"/"+fileID)
		e.mu.Unlock()
		e.wakeFlush()
	}()
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(e.ctx, cancel)
	defer stop()
	defer cancel()
	uploaded, err := e.transferFile(ctx, client, id, r, fileID, path)
	if err != nil || !uploaded {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	current := e.records[r.Run.ID]
	current.Uploaded[fileID] = true
	delete(current.Transfers, fileID)
	_ = e.save(current)
}

func (e *Engine) reserveResult(id string, now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	r := e.records[id]
	if r == nil || r.Ack || r.Cleaned || e.resultRetry[id].After(now) {
		return false
	}
	inflight := 0
	for _, due := range e.resultRetry {
		if due.After(now) {
			inflight++
		}
	}
	if inflight >= resultWindow {
		return false
	}
	e.resultRetry[id] = now.Add(resultRetryDelay)
	return true
}

// Zero means idle: no timer, no filesystem scan, and no network request.
func (e *Engine) nextFlush(now time.Time) time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	var next, resultDue time.Time
	inflight := 0
	earliest := func(current *time.Time, due time.Time) {
		if current.IsZero() || due.Before(*current) {
			*current = due
		}
	}
	for _, due := range e.resultRetry {
		if due.After(now) {
			inflight++
			earliest(&resultDue, due)
		}
	}
	for id, r := range e.records {
		if r.Result == nil || r.Cleaned {
			continue
		}
		if !r.Ack {
			due := e.resultRetry[id]
			if !due.After(now) {
				due = now
				if inflight >= resultWindow {
					due = resultDue
				}
			}
			earliest(&next, due)
			continue
		}
		complete := true
		for fileID := range r.Files {
			if r.Uploaded[fileID] || r.Abandoned {
				if e.transferring[id+"/"+fileID] {
					complete = false
				}
				continue
			}
			complete = false
			if e.transferring[id+"/"+fileID] || len(e.transferring) >= transferConcurrency {
				continue // Worker completion wakes the scheduler; no busy polling.
			}
			checkpoint := r.Transfers[fileID]
			if checkpoint.State == "failed" {
				continue
			}
			due, err := time.Parse(time.RFC3339Nano, checkpoint.RetryAt)
			if err != nil || due.Before(now) {
				due = now
			}
			earliest(&next, due)
		}
		if complete && (r.Result.State != "unknown" || r.Reconciled) {
			due := e.cleanupRetry[id]
			if due.Before(now) {
				due = now
			}
			earliest(&next, due)
		}
	}
	return next
}

func (e *Engine) FlushLoop(ctx context.Context, client *http.Client, id state.Identity) {
	defer e.transferWG.Wait()
	for ctx.Err() == nil && e.ctx.Err() == nil {
		// Coalesce notifications; changes occurring during flush remain queued.
		select {
		case <-e.flushWake:
		default:
		}
		e.flush(ctx, client, id)
		var timer *time.Timer
		var timeout <-chan time.Time
		if due := e.nextFlush(time.Now()); !due.IsZero() {
			timer = time.NewTimer(time.Until(due))
			timeout = timer.C
		}
		select {
		case <-ctx.Done():
		case <-e.ctx.Done():
		case <-e.flushWake:
		case <-timeout:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}
