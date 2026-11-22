package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/SECWG/UIR-Agent/internal/execution"
	"github.com/SECWG/UIR-Agent/internal/protocol"
)

// Serve owns startup and reconnects. Its engine outlives each control connection.
func Serve(ctx context.Context, o Options) error {
	o.instanceID = protocol.NewID()
	o.OnReady = notifyReadyOnce(o.OnReady)
	client, server, err := Client(o)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	id, store, admissionPending, err := prepareStartWithRetry(ctx, client, o, server)
	var duplicate *alreadyOnline
	if errors.As(err, &duplicate) {
		log.Printf("Agent for device %s is already online; this instance will exit", duplicate.deviceID)
		return o.OnReady(duplicate.deviceID, false)
	}
	if err != nil {
		return err
	}
	if store == nil {
		return o.OnReady(id.DeviceID, false)
	}
	defer store.Close()
	if admissionPending {
		admissionCtx, cancelAdmission := context.WithCancel(ctx)
		done := make(chan struct{})
		go func(code string) {
			defer close(done)
			retryAdmission(admissionCtx, client, id, code, o.instanceID)
		}(o.JoinToken)
		defer func() {
			cancelAdmission()
			<-done
		}()
	}
	o.JoinToken = ""
	engine, err := execution.NewEngine(ctx, store)
	if err != nil {
		return err
	}
	defer engine.Close()
	engine.SetToolDownloader(func(ctx context.Context, taskID string) (io.ReadCloser, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", id.Server+"/v1/agent/tasks/"+taskID+"/tool", nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+id.Credential)
		req.Header.Set("X-Device-Id", id.DeviceID)
		res, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		if res.StatusCode != 200 {
			res.Body.Close()
			return nil, fmt.Errorf("tool download HTTP %d", res.StatusCode)
		}
		return res.Body, nil
	})
	mode := "foreground mode · Ctrl+C stops the agent and its active task"
	if o.Background {
		mode = "background mode · use the platform to exit"
	}
	log.Printf("agent %s · device %s · %s", Version, id.DeviceID, mode)
	delay := time.Second
	for ctx.Err() == nil && engine.Err() == nil {
		connectedAt := time.Now()
		established, err := session(ctx, client, id, engine, o)
		err = controlError(err, id.DeviceID)
		if errors.As(err, &duplicate) {
			log.Printf("Agent for device %s is already online; this instance will exit", duplicate.deviceID)
			return o.OnReady(duplicate.deviceID, false)
		}
		var uninstall *UninstallIntent
		if errors.As(err, &uninstall) {
			return uninstall
		}
		if errors.Is(err, errShutdown) {
			log.Print("platform requested exit; stopping active work and preserving device state")
			return nil
		}
		if ctx.Err() != nil {
			break
		}
		if engine.Err() != nil {
			return errors.New("local persistence failed; execution stopped")
		}
		var permanent *authFailure
		var refused *controlRefused
		var readiness *readinessFailure
		if errors.As(err, &permanent) || errors.As(err, &refused) || errors.As(err, &readiness) {
			return err
		}
		if established && time.Since(connectedAt) > 30*time.Second {
			delay = time.Second
		}
		pause := max(time.Second, time.Duration(float64(delay)*(0.5+rand.Float64()*0.5)))
		log.Printf("control disconnected (%v); reconnect in %s, active task keeps its original deadline", err, pause.Round(time.Millisecond))
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
		delay = min(60*time.Second, delay*2)
	}
	if engine.Err() != nil && ctx.Err() == nil {
		return errors.New("local persistence failed; execution stopped")
	}
	return ctx.Err()
}
