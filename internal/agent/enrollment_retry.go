package agent

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/SECWG/UIR-Agent/internal/state"
)

func retryableEnrollment(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	var failure *joinFailure
	if errors.As(err, &failure) {
		return failure.status >= 500 || failure.status == 408 || failure.status == 429 || failure.code == "AGENT_UNINSTALL_PENDING" || failure.code == "AGENT_CONNECTING"
	}
	var network net.Error
	return errors.As(err, &network) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func waitEnrollmentRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func prepareStartWithRetry(ctx context.Context, client *http.Client, o Options, server string) (state.Identity, *state.Store, bool, error) {
	delay := time.Second
	for {
		id, store, admissionPending, err := prepareStart(ctx, client, o, server)
		if err == nil || !retryableEnrollment(err) || ctx.Err() != nil {
			return id, store, admissionPending, err
		}
		log.Printf("enrollment temporarily unavailable (%v); retry in %s with the same device identity", err, delay)
		if err = waitEnrollmentRetry(ctx, delay); err != nil {
			return id, nil, false, err
		}
		delay = min(30*time.Second, delay*2)
	}
}

// A failed conversation admission must not hold the device's control connection offline.
func retryAdmission(ctx context.Context, client *http.Client, id state.Identity, code, instanceID string) {
	delay := time.Second
	for waitEnrollmentRetry(ctx, delay) == nil {
		receipt, err := enrollIdentity(ctx, client, id, code, false, instanceID)
		if err == nil {
			if receipt.ScopeAdded {
				log.Printf("device %s admitted to conversation %s", id.DeviceID, receipt.ConversationID)
			}
			return
		}
		if ctx.Err() != nil {
			return
		}
		if !retryableEnrollment(err) {
			log.Printf("session admission not applied (%v); existing device authorization is unchanged", err)
			return
		}
		delay = min(30*time.Second, delay*2)
	}
}
