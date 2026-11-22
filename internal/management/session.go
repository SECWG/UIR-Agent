package management

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/SECWG/UIR-Agent/internal/protocol"
	"github.com/SECWG/UIR-Agent/internal/state"
)

// Session belongs to one control connection. files is an engine-wide limit:
// a blocking OS read retains its slot across reconnects until the worker returns.
// wg tracks connection-bound response, terminal, and relay workers only.
type Session struct {
	ctx          context.Context
	client       *http.Client
	id           state.Identity
	private      string
	send         func(any) error
	mu           sync.Mutex
	wg           sync.WaitGroup
	terminal     bool
	files        chan struct{}
	directories  directoryCache
	relayEnabled bool
	relays       map[string]struct{}
}

// Options supplies connection-scoped dependencies. FileSlots is owned
// by the runtime and shared across reconnects, including blocked OS reads.
type Options struct {
	Client       *http.Client
	Identity     state.Identity
	PrivateDir   string
	Send         func(any) error
	FileSlots    chan struct{}
	RelayEnabled bool
}

func New(ctx context.Context, o Options) *Session {
	return &Session{ctx: ctx, client: o.Client, id: o.Identity, private: o.PrivateDir,
		send: o.Send, files: o.FileSlots, relayEnabled: o.RelayEnabled}
}

// Wait joins connection workers after the owner cancels the session context.
func (m *Session) Wait() { m.wg.Wait() }

func (m *Session) Receive(raw []byte) (bool, error) {
	var envelope struct {
		Type    string `json:"type"`
		Session string `json:"session_id"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return false, nil
	}
	switch envelope.Type {
	case "relay_open":
		if !protocol.ValidID(envelope.Session) {
			return true, errors.New("invalid relay session")
		}
		return true, m.openRelay(envelope.Session)
	case "host_info_request":
		var request struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &request) != nil || !protocol.ValidID(request.ID) {
			return true, errors.New("invalid host information request")
		}
		return true, m.read(request.ID, "host_info_response", 12*time.Second, 192*1024, "HOST_INFO_TIMEOUT", "HOST_INFO_INVALID", func(ctx context.Context) (any, error) {
			return collectHostInformation(ctx)
		})
	case "fs_request":
		var request fileRequest
		if json.Unmarshal(raw, &request) != nil || !protocol.ValidID(request.ID) {
			return true, errors.New("invalid file request")
		}
		return true, m.read(request.ID, "fs_response", 15*time.Second, 240*1024, "REMOTE_READ_TIMEOUT", "REMOTE_READ_FAILED", func(ctx context.Context) (any, error) {
			return browseFiles(ctx, m.private, request, &m.directories)
		})
	case "terminal_open":
		if !protocol.ValidID(envelope.Session) {
			return true, errors.New("invalid terminal session")
		}
		m.mu.Lock()
		if m.terminal {
			m.mu.Unlock()
			return true, nil
		}
		m.terminal = true
		m.mu.Unlock()
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			defer func() {
				m.mu.Lock()
				m.terminal = false
				m.mu.Unlock()
			}()
			_ = m.serveTerminal(envelope.Session)
		}()
		return true, nil
	}
	return false, nil
}
