package execution

import (
	"context"
	"errors"
	"time"
)

const processStopTimeout = 5 * time.Second

// A successful signal is not proof of exit. Keep Wait in its own goroutine so
// an uninterruptible process cannot hold cancellation or Agent shutdown forever.
func waitProcess(ctx context.Context, done <-chan error, stop func() error, timeout time.Duration) (waitErr, stopErr error, exited bool) {
	select {
	case err := <-done:
		return err, nil, true
	case <-ctx.Done():
	}
	stopErr = stop()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err, stopErr, true
	case <-timer.C:
		return nil, errors.Join(stopErr, errors.New("process exit could not be confirmed after cancellation")), false
	}
}
