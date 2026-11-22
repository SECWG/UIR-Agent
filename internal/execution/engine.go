package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/SECWG/UIR-Agent/internal/protocol"
	"github.com/SECWG/UIR-Agent/internal/state"
)

// Engine owns task execution and durable results across control connections.
// mu protects task state and scheduling maps; flushMu serializes scheduling and
// the shutdown barrier. When both are needed, acquire flushMu before mu.
// sendMu protects only the replaceable control sender, not a network write.
type Engine struct {
	flushMu         sync.Mutex
	flushWake       chan struct{}
	resultRetry     map[string]time.Time
	cleanupRetry    map[string]time.Time
	transferring    map[string]bool
	transferWG      sync.WaitGroup
	downloadTool    func(context.Context, string) (io.ReadCloser, error)
	managementSlots chan struct{}
	ctx             context.Context
	stop            context.CancelFunc
	store           *state.Store
	mu              sync.Mutex
	records         map[string]*state.Record
	active          string
	uninstalling    bool
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	sendMu          sync.RWMutex
	send            func(any) error
}

const maxOutput = 64 << 20

const maxFile = 128 << 20

const maxCache = 512 << 20

const maxPendingTasks = 10000

// NewEngine loads pending tasks without replaying unfinished commands.
// The caller owns s and must close the engine before closing the store.
func NewEngine(ctx context.Context, s *state.Store) (*Engine, error) {
	ctx, stop := context.WithCancel(ctx)
	e := &Engine{
		ctx:          ctx,
		stop:         stop,
		store:        s,
		records:      map[string]*state.Record{},
		flushWake:    make(chan struct{}, 1),
		resultRetry:  map[string]time.Time{},
		cleanupRetry: map[string]time.Time{},
		transferring: map[string]bool{},
	}
	e.managementSlots = make(chan struct{}, 2)
	records, err := s.PendingRecords()
	if err != nil {
		stop()
		return nil, err
	}
	for _, r := range records {
		if r.Result == nil {
			r.Result = protocol.EmptyResult(r.Run.ID, "unknown", "AGENT_RESTART", "Agent restarted before a durable final result; do not automatically repeat this command.")
			r.Result.Output.Stdout.Complete = false
			r.Result.Output.Stderr.Complete = false
			if err = s.Save(r); err != nil {
				stop()
				return nil, err
			}
		}
		e.records[r.Run.ID] = r
	}
	return e, nil
}

// Close cancels execution and waits for task and transfer workers to finish.
func (e *Engine) Close() {
	e.stop()
	// No new transfer can be scheduled after this barrier.
	e.flushMu.Lock()
	e.flushMu.Unlock()
	e.wg.Wait()
	e.transferWG.Wait()
}

func (e *Engine) SetSender(f func(any) error) {
	e.sendMu.Lock()
	e.send = f
	e.sendMu.Unlock()
	e.mu.Lock()
	clear(e.resultRetry)
	e.mu.Unlock()
	e.wakeFlush()
}

func (e *Engine) wakeFlush() {
	select {
	case e.flushWake <- struct{}{}:
	default:
	}
}

// Called with mu held. A queried result also occupies the unacknowledged window.
func (e *Engine) emitResult(r *state.Record) {
	if !r.Ack && !r.Cleaned {
		e.resultRetry[r.Run.ID] = time.Now().Add(resultRetryDelay)
	}
	e.emit(r.Result)
}

func (e *Engine) emit(v any) {
	e.sendMu.RLock()
	f := e.send
	e.sendMu.RUnlock()
	if f != nil {
		_ = f(v)
	}
}

func (e *Engine) save(r *state.Record) error {
	if err := e.store.Save(r); err != nil {
		e.stop()
		return fmt.Errorf("persist task: %w", err)
	}
	return nil
}

func (e *Engine) Receive(raw []byte, clockOffset time.Duration) error {
	var envelope struct {
		Type         string `json:"type"`
		ID           string `json:"task_id"`
		RetryFiles   bool   `json:"retry_files"`
		AbandonFiles bool   `json:"abandon_files"`
		Reconciled   bool   `json:"reconciled"`
	}
	if json.Unmarshal(raw, &envelope) != nil || !protocol.ValidID(envelope.ID) {
		return errors.New("invalid task envelope")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	r := e.records[envelope.ID]
	if r == nil {
		var err error
		r, err = e.store.Record(envelope.ID)
		if err != nil {
			e.stop()
			return fmt.Errorf("read task history: %w", err)
		}
	}
	switch envelope.Type {
	case "run":
		if e.uninstalling {
			return errors.New("AGENT_UNINSTALLING")
		}
		run, err := protocol.DecodeRunMessage(raw)
		if err != nil {
			return err
		}
		normalized, _ := json.Marshal(run)
		sum := sha256.Sum256(normalized)
		hash := hex.EncodeToString(sum[:])
		if r != nil {
			if r.Hash != "" && r.Hash != hash {
				e.emit(protocol.Status(run.ID, "", "TASK_CONFLICT"))
			} else if r.Result != nil {
				e.emitResult(r)
			} else {
				e.emit(protocol.Status(run.ID, r.Phase, ""))
			}
			return nil
		}
		r = &state.Record{Run: run, Hash: hash, Phase: "accepted", Files: map[string]string{}, Uploaded: map[string]bool{}}
		fail := func(code, message string) error {
			r.Result = protocol.EmptyResult(run.ID, "failed", code, message)
			e.records[run.ID] = r
			if err := e.save(r); err != nil {
				return err
			}
			e.emitResult(r)
			e.wakeFlush()
			return nil
		}
		if err := validRun(run); err != nil {
			return fail("INVALID_TASK", err.Error())
		}
		expires, err := time.Parse(time.RFC3339Nano, run.Expires)
		if err != nil || !time.Now().Add(clockOffset).Before(expires) {
			return fail("EXPIRED", "Command expired before execution")
		}
		if e.active != "" {
			return fail("BUSY", "Another task is still running; no command was started")
		}
		size, err := e.store.CacheBytes()
		if err != nil {
			return fail("CACHE_UNAVAILABLE", "Unable to inspect local task storage")
		}
		if size > maxCache-maxOutput*2 || len(e.records) >= maxPendingTasks {
			return fail("CACHE_FULL", "Local result cache is full; recover outstanding results before more execution")
		}
		for _, pending := range e.records {
			if pending.Unresolved() {
				return fail("TASK_STATE_UNKNOWN", "An earlier task has an unknown execution state; verify it in the platform before starting another task")
			}
		}
		if free, err := state.AvailableBytes(e.store.Dir); err != nil || free < 256<<20 {
			return fail("DISK_SPACE", "Insufficient or unknown free disk space")
		}
		e.records[run.ID] = r
		if err = e.save(r); err != nil {
			return err
		}
		taskCtx, cancel := context.WithTimeout(e.ctx, time.Duration(run.Timeout)*time.Millisecond)
		e.active = run.ID
		e.cancel = cancel
		e.emit(protocol.Status(run.ID, "accepted", ""))
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			defer cancel()
			e.execute(taskCtx, r)
		}()
	case "query":
		if r == nil {
			e.emit(protocol.Status(envelope.ID, "not_found", ""))
		} else if r.Result != nil {
			e.emitResult(r)
			if envelope.RetryFiles && !r.Cleaned && !r.Abandoned {
				for fileID, saved := range r.Transfers {
					if !r.Uploaded[fileID] && !e.transferring[r.Run.ID+"/"+fileID] && (saved.State == "failed" || saved.State == "retrying") {
						r.Transfers[fileID] = state.TransferCheckpoint{Transfer: protocol.Transfer{FileID: fileID, State: "uploading"}}
					}
				}
				if err := e.save(r); err != nil {
					return err
				}
				e.wakeFlush()
			}
			for fileID, saved := range r.Transfers {
				if !r.Uploaded[fileID] {
					e.emitTransfer(r.Run.ID, saved.Transfer)
				}
			}
		} else {
			e.emit(protocol.Status(envelope.ID, r.Phase, ""))
		}
	case "cancel":
		if r == nil {
			r = &state.Record{
				Run:      protocol.Run{ID: envelope.ID},
				Result:   protocol.EmptyResult(envelope.ID, "canceled", "", ""),
				Files:    map[string]string{},
				Uploaded: map[string]bool{},
			}
			e.records[envelope.ID] = r
			if err := e.save(r); err != nil {
				return err
			}
		}
		if r.Result != nil {
			e.emitResult(r)
			e.wakeFlush()
		} else if e.active == envelope.ID {
			r.Phase = "stopping"
			if err := e.save(r); err != nil {
				return err
			}
			e.cancel()
			e.emit(protocol.Status(envelope.ID, "stopping", ""))
		}
	case "ack":
		if r != nil &&
			r.Result != nil &&
			(!r.Ack ||
				(envelope.AbandonFiles &&
					!r.Abandoned) ||
				(envelope.Reconciled &&
					!r.Reconciled &&
					r.Result.State == "unknown")) {
			r.Ack = true
			if envelope.AbandonFiles && r.Result.State != "unknown" {
				r.Abandoned = true
			}
			if envelope.Reconciled && r.Result.State == "unknown" {
				r.Reconciled = true
			}
			if err := e.save(r); err != nil {
				return err
			}
			delete(e.resultRetry, r.Run.ID)
			e.wakeFlush()
		}
	default:
		return errors.New("unsupported server message")
	}
	return nil
}
