package agent

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/SECWG/UIR-Agent/internal/state"

	bolt "go.etcd.io/bbolt"
)

// Identity updates are serialized independently of the running task store.
// A repeated start may admit another conversation, but cannot take over live work.
// The third result requests a background admission retry after startup; it is not stored.
func prepareStart(ctx context.Context, client *http.Client, o Options, server string) (state.Identity, *state.Store, bool, error) {
	var id state.Identity
	if err := state.PrepareDirectory(o.DataDir); err != nil {
		return id, nil, false, err
	}
	unlock, err := state.IdentityLock(ctx, o.DataDir)
	if err != nil {
		return id, nil, false, err
	}
	defer unlock()
	id, err = state.LoadIdentity(o.DataDir, server, o.Recover)
	if err != nil {
		return id, nil, false, err
	}
	if id.Server != server {
		probe, probeErr := state.OpenStore(o.DataDir)
		if probeErr != nil {
			return id, nil, false, fmt.Errorf("stop the existing Agent before changing the connection address: %w", probeErr)
		}
		probe.Close()
		id.Server = server
	}
	codeHash := fmt.Sprintf("%x", sha256.Sum256([]byte(o.JoinToken)))
	pendingPath := filepath.Join(o.DataDir, "recovery.pending.json")
	pending, pendingErr := state.ReadIdentity(pendingPath, server)
	if pendingErr == nil && id.Joined && !id.Reenrolling &&
		pending.Credential == id.Credential && pending.RecoveryCodeHash == id.RecoveryCodeHash {
		// The validated receipt was committed; only cleanup was interrupted.
		if err = os.Remove(pendingPath); err != nil {
			return id, nil, false, err
		}
		pendingErr = os.ErrNotExist
	}
	if pendingErr == nil {
		if o.JoinToken == "" || pending.Reenrolling == o.Recover {
			return id, nil, false, errors.New("identity update is pending; retry the original connection or recovery command")
		}
		return finishIdentityUpdate(ctx, client, o, id, &pending, codeHash, pending.Reenrolling)
	}
	if !errors.Is(pendingErr, os.ErrNotExist) {
		return id, nil, false, pendingErr
	}
	completedRecovery := o.Recover && id.Joined && id.RecoveryCodeHash == codeHash
	if o.Recover && !completedRecovery {
		return finishIdentityUpdate(ctx, client, o, id, nil, codeHash, false)
	}

	var admissionErr error
	if o.JoinToken != "" {
		receipt, joinErr := enrollIdentity(ctx, client, id, o.JoinToken, completedRecovery, o.instanceID)
		var failure *joinFailure
		if !completedRecovery && errors.As(joinErr, &failure) &&
			(failure.code == "DEVICE_REENROLLMENT_REQUIRED" || failure.code == "DEVICE_IDENTITY_RECOVERY_REQUIRED") {
			return finishIdentityUpdate(ctx, client, o, id, nil, codeHash, true)
		}
		if joinErr != nil {
			var duplicate *alreadyOnline
			deviceRefused := errors.As(joinErr, &failure) &&
				(failure.code == "DEVICE_AUTH_FAILED" || failure.code == "AGENT_UNINSTALL_PENDING" ||
					failure.code == "DEVICE_REENROLLMENT_CODE_REQUIRED" || failure.code == "AGENT_CONNECTING")
			// A connection claim is not a conversation-admission error: reconnecting
			// with the old identity here would race the authorized joining instance.
			if completedRecovery || !id.Joined || deviceRefused || errors.As(joinErr, &duplicate) || ctx.Err() != nil {
				return id, nil, false, joinErr
			}
			admissionErr = joinErr
			log.Printf("session admission not applied (%v); reconnecting the existing device only", joinErr)
		} else {
			id.DeviceID, id.Joined, id.Reenrolling = receipt.DeviceID, true, false
			if err = state.WriteIdentity(filepath.Join(o.DataDir, "identity.json"), id); err != nil {
				return id, nil, false, err
			}
			if receipt.ScopeAdded {
				log.Printf("device %s admitted to conversation %s", id.DeviceID, receipt.ConversationID)
			}
			if receipt.AdmissionError != "" {
				log.Printf("device %s enrolled; session admission not applied (%s)", id.DeviceID, receipt.AdmissionError)
			}
		}
	} else if !id.Joined {
		return id, nil, false, errors.New("first connection requires --join-token")
	}
	store, err := state.OpenStore(o.DataDir)
	if errors.Is(err, bolt.ErrTimeout) {
		if retryableEnrollment(admissionErr) {
			return id, nil, false, admissionErr
		}
		log.Printf("Agent for device %s is already running; its connection and active tasks are unchanged", id.DeviceID)
		return id, nil, false, nil
	}
	if err != nil {
		return id, nil, false, err
	}
	if err = store.Joined(id); err == nil {
		err = state.WriteIdentity(filepath.Join(o.DataDir, "identity.json"), id)
	}
	if err != nil {
		store.Close()
		return id, nil, false, err
	}
	return id, store, retryableEnrollment(admissionErr), nil
}
