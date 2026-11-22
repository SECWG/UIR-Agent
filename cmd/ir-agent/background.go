package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/SECWG/UIR-Agent/internal/agent"
)

const backgroundMarker = "UIR_AGENT_BACKGROUND_CHILD"

type startupResult struct {
	DeviceID string `json:"deviceId,omitempty"`
	PID      int    `json:"pid,omitempty"`
	Running  bool   `json:"running"`
	Error    string `json:"error,omitempty"`
}

// The parent owns only startup pipes, never the identity/database lock. The child
// waits for acknowledgement after platform confirmation and before accepting work,
// so failed startup can be terminated without interrupting a newly accepted task.
func startBackground(ctx context.Context, o agent.Options) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"--foreground", "--server", o.Server, "--data-dir", o.DataDir}
	if o.JoinToken != "" {
		args = append(args, "--join-token", "-")
	}
	if o.Proxy != "" {
		args = append(args, "--proxy", o.Proxy)
	}
	if o.Recover {
		args = append(args, "--recover")
	}
	if o.DisableRelay {
		args = append(args, "--relay=false")
	}
	cmd := exec.Command(executable, args...)
	for _, value := range os.Environ() {
		if !strings.EqualFold(strings.SplitN(value, "=", 2)[0], backgroundMarker) {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, backgroundMarker+"=1")
	detach(cmd)
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	defer output.Close()
	if err := cmd.Start(); err != nil {
		return err
	}
	started := false
	defer func() {
		if !started {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	if o.JoinToken != "" {
		if _, err := fmt.Fprintln(input, o.JoinToken); err != nil {
			return err
		}
	}
	ready := make(chan startupResult, 1)
	go func() {
		var result startupResult
		if err := json.NewDecoder(io.LimitReader(output, 8192)).Decode(&result); err != nil {
			result.Error = "background initialization failed: " + err.Error()
		}
		ready <- result
	}()
	// Cover a 60-second connection claim, enrollment backoff, and the handshake.
	// On timeout the deferred Kill/Wait also stops the joining child.
	timer := time.NewTimer(150 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("background startup timed out; check platform connectivity and retry using the same data directory")
	case result := <-ready:
		if result.Error != "" {
			return errors.New(result.Error)
		}
		if result.DeviceID == "" {
			return errors.New("background startup returned no device identity")
		}
		if !result.Running {
			fmt.Printf("Agent for device %s is already running; connection and identity unchanged.\n", result.DeviceID)
			return nil
		}
		if _, err := fmt.Fprintln(input, "continue"); err != nil {
			return err
		}
		started = true
		_ = cmd.Process.Release()
		fmt.Printf("Agent started in background · PID %d · device %s\nLog: %s\nUse the platform device menu to exit.\n", result.PID, result.DeviceID, filepath.Join(o.DataDir, "agent.log"))
		return nil
	}
}

// Startup logs are bounded and buffered until Serve holds the exclusive state
// lock. A repeated launch must never rotate or truncate the running process log.
type startupLog struct{ data []byte }

func (b *startupLog) Write(p []byte) (int, error) {
	n := len(p)
	b.data = append(b.data, p...)
	if len(b.data) > 32768 {
		b.data = b.data[len(b.data)-32768:]
	}
	return n, nil
}

func serveBackground(ctx context.Context, o agent.Options) error {
	buffer := &startupLog{}
	log.SetOutput(buffer)
	var logs *backgroundLogs
	var reported bool
	defer func() {
		log.SetOutput(os.Stderr)
		if logs != nil {
			// Keep runtime crash output alive if Serve unwinds through a panic.
			_ = logs.output.Close()
		}
	}()
	o.Background = true
	o.OnReady = func(deviceID string, running bool) error {
		if running {
			var err error
			logs, err = openBackgroundLogs(o.DataDir)
			if err != nil {
				return fmt.Errorf("open background logs: %w", err)
			}
			// SetOutput synchronizes with any enrollment log writer.
			log.SetOutput(logs.output)
			if _, err := logs.output.Write(buffer.data); err != nil {
				return err
			}
		}
		if err := json.NewEncoder(os.Stdout).Encode(startupResult{DeviceID: deviceID, PID: os.Getpid(), Running: running}); err != nil {
			return err
		}
		reported = true
		if !running {
			return nil
		}
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() || scanner.Text() != "continue" {
			return errors.New("background startup was canceled by launcher")
		}
		_ = os.Stdin.Close()
		_ = os.Stdout.Close()
		return nil
	}
	err := agent.Serve(ctx, o)
	if !reported {
		message := "background initialization ended before readiness"
		if err != nil {
			message = err.Error()
		}
		_ = json.NewEncoder(os.Stdout).Encode(startupResult{Error: message})
	}
	if err != nil {
		log.Printf("agent stopped: %v", err)
	} else {
		log.Print("agent stopped")
	}
	if logs != nil {
		if closeErr := logs.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close background logs: %w", closeErr))
		}
	}
	return err
}
