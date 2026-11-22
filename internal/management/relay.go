package management

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const relayLimit = 16
const relayChunk = 32 * 1024

var relayHostname = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9.-]*[a-zA-Z0-9])?$`)

func validRelayAddress(host string, port int) bool {
	return port > 0 && port <= 65535 && len(host) > 0 && len(host) <= 253 && (net.ParseIP(host) != nil || relayHostname.MatchString(host))
}

func (m *Session) openRelay(id string) error {
	respond := func(code string) error {
		return m.send(map[string]any{"type": "relay_error", "session_id": id, "error": code})
	}
	if !m.relayEnabled {
		return respond("RELAY_UNAVAILABLE")
	}
	m.mu.Lock()
	if _, ok := m.relays[id]; ok {
		m.mu.Unlock()
		return nil
	}
	if len(m.relays) >= relayLimit {
		m.mu.Unlock()
		return respond("RELAY_BUSY")
	}
	if m.relays == nil {
		m.relays = make(map[string]struct{})
	}
	m.relays[id] = struct{}{}
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		defer func() {
			m.mu.Lock()
			delete(m.relays, id)
			m.mu.Unlock()
		}()
		if err := m.serveRelay(id); err != nil && m.ctx.Err() == nil {
			_ = respond("RELAY_CONNECTION_LOST")
		}
	}()
	return nil
}

// 平台通过当前认证控制连接发起会话；目标只来自随后认证的数据通道。
// 字节直接流式转发，不落盘，不进入本机执行队列，也不处理 SSH 凭据。
func (m *Session) serveRelay(id string) error {
	ctx, cancel := context.WithTimeout(m.ctx, 8*time.Hour)
	defer cancel()
	dialCtx, stop := context.WithTimeout(ctx, 15*time.Second)
	c, _, err := websocket.Dial(dialCtx, strings.Replace(m.id.Server, "http", "ws", 1)+"/v1/agent/relay/"+id, &websocket.DialOptions{
		HTTPClient: m.client, HTTPHeader: http.Header{"Authorization": {"Bearer " + m.id.Credential}, "X-Device-Id": {m.id.DeviceID}}, Subprotocols: []string{"wg-ir.relay.v1"},
	})
	stop()
	if err != nil {
		return err
	}
	defer c.CloseNow()
	if c.Subprotocol() != "wg-ir.relay.v1" {
		return errors.New("unsupported relay protocol")
	}
	c.SetReadLimit(256 * 1024)
	startCtx, finish := context.WithTimeout(ctx, 10*time.Second)
	kind, data, err := c.Read(startCtx)
	finish()
	var request struct {
		Type string `json:"type"`
		Host string `json:"host"`
		Port int    `json:"port"`
	}
	if err != nil ||
		kind != websocket.MessageText ||
		len(data) > 4096 ||
		json.Unmarshal(data, &request) != nil ||
		request.Type != "connect" ||
		!validRelayAddress(request.Host, request.Port) {
		return errors.New("invalid relay target")
	}
	targetCtx, targetDone := context.WithTimeout(ctx, 10*time.Second)
	target, err := (&net.Dialer{KeepAlive: 30 * time.Second}).DialContext(targetCtx, "tcp", net.JoinHostPort(request.Host, strconv.Itoa(request.Port)))
	targetDone()
	if err != nil {
		code := "RELAY_CONNECT_FAILED"
		if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
			code = "RELAY_CONNECT_TIMEOUT"
		}
		body, _ := json.Marshal(map[string]string{"type": "error", "code": code})
		writeCtx, done := context.WithTimeout(ctx, 5*time.Second)
		_ = c.Write(writeCtx, websocket.MessageText, body)
		done()
		return nil
	}
	defer target.Close()
	writeCtx, done := context.WithTimeout(ctx, 5*time.Second)
	err = c.Write(writeCtx, websocket.MessageText, []byte(`{"type":"ready"}`))
	done()
	if err != nil {
		return err
	}
	remote := websocket.NetConn(ctx, c, websocket.MessageBinary)
	c.SetReadLimit(256 * 1024)
	finished := make(chan error, 2)
	go func() { finished <- copyRelay(target, remote) }()
	go func() { finished <- copyRelay(remote, target) }()
	received := 0
	select {
	case <-ctx.Done():
		err = ctx.Err()
	case err = <-finished:
		// 一侧结束即关闭整条 SSH 传输，不重连或重放任何字节。
		received = 1
	}
	cancel()
	_ = target.Close()
	_ = c.CloseNow()
	for received < 2 {
		<-finished
		received++
	}
	return err
}

func copyRelay(dst io.Writer, src io.Reader) error {
	buf := make([]byte, relayChunk)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			written, writeErr := dst.Write(buf[:n])
			if writeErr != nil {
				return writeErr
			}
			if written != n {
				return io.ErrShortWrite
			}
		}
		if err != nil {
			return err
		}
	}
}
