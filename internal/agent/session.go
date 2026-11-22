package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os/user"
	"runtime"
	"strings"
	"time"

	"github.com/SECWG/UIR-Agent/internal/execution"
	"github.com/SECWG/UIR-Agent/internal/management"
	"github.com/SECWG/UIR-Agent/internal/protocol"
	"github.com/SECWG/UIR-Agent/internal/state"
	"github.com/SECWG/UIR-Agent/internal/terminal"

	"github.com/coder/websocket"
)

type authFailure struct{ status int }

func (e *authFailure) Error() string {
	return fmt.Sprintf("device access refused (HTTP %d); check device credentials and access authorization; after uninstall, use a newly generated connection command", e.status)
}

func session(ctx context.Context, client *http.Client, id state.Identity, e *execution.Engine, o Options) (bool, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	headers := http.Header{"Authorization": {"Bearer " + id.Credential}, "X-Device-Id": {id.DeviceID}}
	if o.instanceID != "" {
		headers.Set("X-Agent-Instance-Id", o.instanceID)
	}
	c, res, err := websocket.Dial(dialCtx, strings.Replace(id.Server, "http", "ws", 1)+"/v1/agent/channel", &websocket.DialOptions{
		HTTPClient:   client,
		HTTPHeader:   headers,
		Subprotocols: []string{"wg-ir.v1"},
	})
	cancel()
	if err != nil {
		return false, dialFailure(err, res, id.DeviceID)
	}
	defer c.CloseNow()
	if c.Subprotocol() != "wg-ir.v1" {
		return false, errors.New("unsupported channel protocol")
	}
	c.SetReadLimit(protocol.MaxRunWireBytes)
	sessionCtx, end := context.WithCancel(e.Context())
	defer end()
	write := func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		writeCtx, stop := context.WithTimeout(sessionCtx, 10*time.Second)
		defer stop()
		return c.Write(writeCtx, websocket.MessageText, b)
	}
	hostname, _ := management.Hostname()
	username := ""
	if u, err := user.Current(); err == nil {
		username = u.Username
	}
	hello := map[string]any{
		"type":             "hello",
		"version":          Version,
		"os":               runtime.GOOS,
		"arch":             runtime.GOARCH,
		"shells":           terminal.Shells(),
		"hostname":         hostname,
		"user":             username,
		"management":       true,
		"terminal":         terminal.Available(),
		"host_info":        true,
		"uninstall":        uninstallAvailable(),
		"shutdown":         true,
		"relay":            !o.DisableRelay,
		"command_encoding": protocol.CommandEncoding,
	}
	if key := state.InstallationKey(id.Server); key != "" {
		hello["installation_key"] = key
	}
	if err = write(hello); err != nil {
		return false, err
	}
	if err = awaitConnectionReady(sessionCtx, c, o.instanceID); err != nil {
		return false, err
	}
	if o.OnReady != nil {
		if err = o.OnReady(id.DeviceID, true); err != nil {
			return false, err
		}
	}
	send, senderDone := protocol.QueuedControlSender(sessionCtx, write, func() {
		end()
		c.CloseNow()
	})
	defer func() {
		end()
		<-senderDone
	}()
	log.Print("control connected")
	pingDone := make(chan struct{})
	defer func() {
		end()
		<-pingDone
	}()
	go func() {
		defer close(pingDone)
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-sessionCtx.Done():
				return
			case <-tick.C:
				pingCtx, stop := context.WithTimeout(sessionCtx, 10*time.Second)
				err := c.Ping(pingCtx)
				stop()
				if err != nil {
					end()
					c.CloseNow()
					return
				}
			}
		}
	}()
	var clockOffset time.Duration
	if res != nil {
		if date, er := http.ParseTime(res.Header.Get("Date")); er == nil {
			clockOffset = time.Until(date)
		}
	}
	e.SetSender(send)
	defer e.SetSender(nil)
	manual := management.New(sessionCtx, management.Options{
		Client: client, Identity: id, PrivateDir: e.DataDir(),
		Send: send, FileSlots: e.ManagementReadSlots(), RelayEnabled: !o.DisableRelay,
	})
	defer func() {
		end()
		manual.Wait()
	}()
	done := make(chan struct{})
	defer func() {
		end()
		<-done
	}()
	go func() {
		defer close(done)
		e.FlushLoop(sessionCtx, client, id)
	}()
	for {
		kind, raw, er := c.Read(sessionCtx)
		if er != nil {
			return true, er
		}
		if kind != websocket.MessageText {
			return true, errors.New("binary control message rejected")
		}
		var envelope struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		decoded := json.Unmarshal(raw, &envelope) == nil
		if decoded && envelope.Type == "shutdown_request" {
			if err := validateShutdown(raw); err != nil {
				return true, err
			}
			// Acknowledge before closing the authenticated control channel.
			if err := write(map[string]any{"type": "shutdown_response", "id": envelope.ID, "result": map[string]any{"accepted": true}}); err != nil {
				return true, err
			}
			return true, errShutdown
		}
		if decoded && envelope.Type == "uninstall_request" {
			intent, uninstallErr := prepareUninstall(e, raw, o, id)
			if uninstallErr != nil {
				if err = send(map[string]any{"type": "uninstall_response", "id": envelope.ID, "error": uninstallErr.Error()}); err != nil {
					return true, err
				}
				continue
			}
			// Uninstall must reach the socket before the session and local identity are removed.
			if err = write(map[string]any{"type": "uninstall_response", "id": envelope.ID, "result": map[string]bool{"accepted": true}}); err != nil {
				intent.abort()
				e.CancelUninstall()
				return true, err
			}
			return true, intent
		}
		if handled, err := manual.Receive(raw); handled {
			if err != nil {
				return true, err
			}
			continue
		}
		if er = e.Receive(raw, clockOffset); er != nil {
			return true, er
		}
	}
}
