package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

type alreadyOnline struct{ deviceID string }

func (*alreadyOnline) Error() string { return "AGENT_ALREADY_ONLINE" }

type controlRefused struct{ code string }

func (e *controlRefused) Error() string { return e.code }

type readinessFailure struct{ err error }

func (e *readinessFailure) Error() string { return e.err.Error() }
func (e *readinessFailure) Unwrap() error { return e.err }

// Only network failures and temporary claims are retried. A replacement/credential
// refusal must not create a reconnection fight with the authorized live process.
func controlError(err error, deviceID string) error {
	var closed websocket.CloseError
	if !errors.As(err, &closed) {
		return err
	}
	switch closed.Code {
	case 4009:
		return &alreadyOnline{deviceID: deviceID}
	case 4001:
		if closed.Reason == "CONNECTION_REPLACED" {
			return &alreadyOnline{deviceID: deviceID}
		}
		if closed.Reason == "CREDENTIAL_CHANGED" || closed.Reason == "CREDENTIAL_EXPIRED" {
			return &controlRefused{code: closed.Reason}
		}
		return &controlRefused{code: "CONTROL_ACCESS_REVOKED"}
	default:
		return err // Includes 4008 AGENT_CONNECTING; retain the normal bounded backoff.
	}
}

func dialFailure(err error, response *http.Response, deviceID string) error {
	if response == nil {
		return controlError(err, deviceID)
	}
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return &authFailure{response.StatusCode}
	}
	if response.StatusCode == 409 && response.Body != nil {
		defer response.Body.Close()
		var failure struct {
			Code string `json:"code"`
		}
		if json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&failure) == nil && failure.Code == "AGENT_ALREADY_ONLINE" {
			return &alreadyOnline{deviceID: deviceID}
		}
	}
	return err
}

func awaitConnectionReady(ctx context.Context, c *websocket.Conn, instanceID string) error {
	if instanceID == "" {
		return nil
	} // Direct helper calls can exercise the legacy protocol.
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	kind, raw, err := c.Read(waitCtx)
	if err != nil {
		return err
	}
	var ready struct {
		Type       string `json:"type"`
		InstanceID string `json:"instance_id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if kind != websocket.MessageText || decoder.Decode(&ready) != nil || decoder.Decode(new(any)) != io.EOF || ready.Type != "connection_ready" || ready.InstanceID != instanceID {
		return &controlRefused{code: "INVALID_CONNECTION_CONFIRMATION"}
	}
	return nil
}

func notifyReadyOnce(callback func(string, bool) error) func(string, bool) error {
	reported := false
	return func(deviceID string, running bool) error {
		if reported {
			return nil
		}
		reported = true
		if callback == nil {
			return nil
		}
		if err := callback(deviceID, running); err != nil {
			return &readinessFailure{err: fmt.Errorf("startup confirmation: %w", err)}
		}
		return nil
	}
}
