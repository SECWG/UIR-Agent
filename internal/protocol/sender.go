package protocol

import (
	"context"
	"errors"
)

const ControlQueueSize = 32

// A single bounded writer keeps socket backpressure out of command admission and
// cancellation. Failure closes this session; durable results are resent on reconnect.
func QueuedControlSender(ctx context.Context, write func(any) error, disconnect func()) (func(any) error, <-chan struct{}) {
	queue := make(chan any, ControlQueueSize)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case value := <-queue:
				if ctx.Err() != nil {
					return
				}
				if err := write(value); err != nil {
					disconnect()
					return
				}
			}
		}
	}()
	return func(value any) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return errors.New("control sender stopped")
		default:
		}
		select {
		case queue <- value:
			return nil
		default:
			disconnect()
			return errors.New("control output queue full")
		}
	}, done
}
