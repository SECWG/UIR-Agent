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

	"github.com/SECWG/UIR-Agent/internal/management"
	"github.com/SECWG/UIR-Agent/internal/protocol"
	"github.com/SECWG/UIR-Agent/internal/state"
)

func enroll(ctx context.Context, client *http.Client, id state.Identity, joinToken string) error {
	_, err := enrollIdentity(ctx, client, id, joinToken, false, "")
	return err
}

type joinReceipt struct {
	Action         string `json:"action"`
	DeviceID       string `json:"deviceId"`
	ConversationID string `json:"conversationId"`
	ScopeAdded     bool   `json:"scopeAdded"`
	Recovered      bool   `json:"recovered"`
	Reenrolled     bool   `json:"reenrolled"`
	AdmissionError string `json:"admissionError"`
}

type joinFailure struct {
	code   string
	status int
}

func (e *joinFailure) Error() string {
	message := fmt.Sprintf("join HTTP %d (%s)", e.status, e.code)
	if e.code == "JOIN_CODE_EXPIRED" || e.code == "DEVICE_REENROLLMENT_CODE_REQUIRED" {
		message += "; generate a new connection command in the platform"
	}
	return message
}

func enrollIdentity(ctx context.Context, client *http.Client, id state.Identity, joinToken string, recover bool, instanceID string) (joinReceipt, error) {
	var ack joinReceipt
	hostname, _ := management.Hostname()
	if hostname == "" {
		hostname = "IR endpoint"
	}
	body := map[string]any{"deviceId": id.DeviceID, "credential": id.Credential, "hostname": hostname}
	if instanceID != "" {
		if !protocol.ValidID(instanceID) {
			return ack, errors.New("invalid Agent instance ID")
		}
		body["instanceId"] = instanceID
	}
	if key := state.InstallationKey(id.Server); key != "" {
		body["installationKey"] = key
	}
	if id.Reenrolling {
		body["reenroll"] = true
	}
	if recover {
		body["recover"] = true
	}
	raw, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", id.Server+"/v1/agent/join", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+joinToken)
	req.Header.Set("Content-Type", "application/json")
	res, e := client.Do(req)
	if e != nil {
		return ack, fmt.Errorf("join connection failed: %w", e)
	}
	defer res.Body.Close()
	b, e := io.ReadAll(io.LimitReader(res.Body, 4096))
	if e != nil {
		return ack, e
	}
	if res.StatusCode != 200 {
		var failure struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(b, &failure) == nil && failure.Code != "" {
			return ack, &joinFailure{failure.Code, res.StatusCode}
		}
		return ack, &joinFailure{"JOIN_REQUEST_FAILED", res.StatusCode}
	}
	if json.Unmarshal(b, &ack) != nil || !protocol.ValidID(ack.DeviceID) ||
		(ack.Action != "" && ack.Action != "connect" && ack.Action != "already_online") {
		return ack, errors.New("invalid join acknowledgment")
	}
	if ack.Action == "already_online" {
		// This is a display-only identity, not permission to persist our candidate secret.
		return ack, &alreadyOnline{deviceID: ack.DeviceID}
	}
	if (recover && (!ack.Recovered || ack.Reenrolled)) ||
		(!recover && (ack.Recovered || (ack.DeviceID != id.DeviceID && !(id.Reenrolling && ack.Reenrolled)))) ||
		(id.Reenrolling && !ack.Reenrolled) {
		return ack, errors.New("invalid join acknowledgment")
	}
	return ack, nil
}
