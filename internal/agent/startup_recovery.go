package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/SECWG/UIR-Agent/internal/state"
)

// Keep candidate credentials separate until the platform acknowledges them.
// Both explicit recovery and automatic reenrollment retry the same pending secret,
// including when the server committed an update but its response was lost.
func finishIdentityUpdate(ctx context.Context, client *http.Client, o Options, original state.Identity, saved *state.Identity, codeHash string, reenroll bool) (state.Identity, *state.Store, bool, error) {
	if o.JoinToken == "" {
		return original, nil, false, errors.New("identity update requires --join-token")
	}
	store, err := state.OpenStore(o.DataDir)
	if err != nil {
		return original, nil, false, fmt.Errorf("stop the existing Agent before updating its identity: %w", err)
	}
	keepStore := false
	defer func() {
		if !keepStore {
			store.Close()
		}
	}()
	pending := state.Identity{Server: original.Server, DeviceID: original.DeviceID, Reenrolling: reenroll}
	if saved != nil {
		pending = *saved
	} else {
		// Preserve the original in both durable locations before the first request.
		if err = store.SaveIdentity(original); err != nil {
			return original, nil, false, err
		}
		pending.Credential = state.NewSecret()
	}
	pending.Server, pending.RecoveryCodeHash = original.Server, codeHash
	pendingPath := filepath.Join(o.DataDir, "recovery.pending.json")
	if err = state.WriteIdentity(pendingPath, pending); err != nil {
		return original, nil, false, err
	}
	receipt, err := enrollIdentity(ctx, client, pending, o.JoinToken, !reenroll, o.instanceID)
	if err != nil {
		var failure *joinFailure
		var duplicate *alreadyOnline
		denied := errors.As(err, &duplicate) ||
			(errors.As(err, &failure) && failure.status >= 400 && failure.status < 500 && !retryableEnrollment(err))
		// Only a definitive first denial proves these credentials were not adopted.
		// Temporary claims and unknown outcomes retain the candidate for replay.
		if saved == nil && denied {
			if removeErr := os.Remove(pendingPath); removeErr != nil {
				return original, nil, false, fmt.Errorf("discard unused pending identity: %w (join failed: %v)", removeErr, err)
			}
		}
		return original, nil, false, err
	}
	pending.DeviceID, pending.Joined, pending.Reenrolling = receipt.DeviceID, true, false
	if err = store.Joined(pending); err == nil {
		err = state.WriteIdentity(filepath.Join(o.DataDir, "identity.json"), pending)
	}
	if err == nil {
		err = os.Remove(pendingPath)
	}
	if err != nil {
		return original, nil, false, err
	}
	log.Printf("device %s identity acknowledged; original device history retained", pending.DeviceID)
	keepStore = true
	return pending, store, false, nil
}
