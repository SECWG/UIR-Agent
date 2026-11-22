package management

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Slots belong to the Engine, not a control connection. A blocked OS read keeps
// its slot until it really ends, while its response waiter can time out or leave
// with the session. Reconnecting must not create another set of blocked workers.
func (m *Session) read(id, kind string, timeout time.Duration, limit int, timeoutCode, invalidCode string, read func(context.Context) (any, error)) error {
	if err := m.ctx.Err(); err != nil {
		return err
	}
	select {
	case m.files <- struct{}{}:
	default:
		return m.send(map[string]any{"type": kind, "id": id, "error": "REMOTE_BUSY"})
	}
	ctx, cancel := context.WithTimeout(m.ctx, timeout)
	type outcome struct {
		value any
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		defer func() { <-m.files }()
		value, err := read(ctx)
		done <- outcome{value, err}
	}()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer cancel()
		var result outcome
		select {
		case result = <-done:
		case <-ctx.Done():
		}
		if m.ctx.Err() != nil {
			return // Late results belong to the old session and must be discarded.
		}
		if ctx.Err() != nil {
			result.err = errors.New(timeoutCode)
		}
		reply := map[string]any{"type": kind, "id": id}
		if result.err != nil {
			reply["error"] = result.err.Error()
		} else {
			reply["result"] = result.value
		}
		if encoded, err := json.Marshal(reply); err != nil || len(encoded) > limit {
			delete(reply, "result")
			reply["error"] = invalidCode
		}
		_ = m.send(reply)
	}()
	return nil
}
