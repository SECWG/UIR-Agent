package management

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/SECWG/UIR-Agent/internal/terminal"

	"github.com/coder/websocket"
)

type terminalMessage struct {
	Type  string `json:"type"`
	Shell string `json:"shell"`
	Data  string `json:"data"`
	Cols  int    `json:"cols"`
	Rows  int    `json:"rows"`
	Bytes int    `json:"bytes"`
}

func (m *Session) serveTerminal(id string) error {
	ctx, cancel := context.WithCancel(m.ctx)
	defer cancel()
	dial, stop := context.WithTimeout(ctx, 15*time.Second)
	c, _, err := websocket.Dial(dial, strings.Replace(m.id.Server, "http", "ws", 1)+"/v1/agent/terminal/"+id, &websocket.DialOptions{
		HTTPClient:   m.client,
		HTTPHeader:   http.Header{"Authorization": {"Bearer " + m.id.Credential}, "X-Device-Id": {m.id.DeviceID}},
		Subprotocols: []string{"wg-ir.terminal.v1"},
	})
	stop()
	if err != nil {
		return err
	}
	defer c.CloseNow()
	if c.Subprotocol() != "wg-ir.terminal.v1" {
		return errors.New("unsupported terminal protocol")
	}
	if !terminal.Available() {
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"error","code":"AGENT_TERMINAL_UNAVAILABLE"}`))
		return errors.New("interactive terminal unavailable")
	}
	c.SetReadLimit(65536)
	first, stop := context.WithTimeout(ctx, 10*time.Second)
	kind, raw, err := c.Read(first)
	stop()
	var start terminalMessage
	if err != nil ||
		kind != websocket.MessageText ||
		json.Unmarshal(raw, &start) != nil ||
		start.Type != "start" ||
		!terminal.ValidSize(start.Cols, start.Rows) ||
		!slices.Contains(terminal.Shells(), start.Shell) {
		return errors.New("invalid terminal start")
	}
	process, err := terminal.Open(start.Shell, start.Cols, start.Rows)
	if err != nil {
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"error","code":"TERMINAL_START_FAILED"}`))
		return err
	}
	defer func() {
		cancel()
		_ = c.CloseNow()
		process.Close()
	}()
	if err = c.Write(ctx, websocket.MessageText, []byte(`{"type":"ready"}`)); err != nil {
		return err
	}
	// 独立数据连接按浏览器实际消费量限流，避免输出淹没控制、取消及文件回传。
	credit := make(chan struct{}, 1)
	var consumed atomic.Int64
	input := make(chan string, 8)
	go func() {
		defer cancel()
		defer c.CloseNow()
		for {
			select {
			case data := <-input:
				if _, err := process.Write([]byte(data)); err != nil {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		defer cancel()
		defer c.CloseNow()
		pending := 0
		buffer := make([]byte, 16384)
		for {
			pending -= int(consumed.Swap(0))
			for pending >= 128*1024 {
				select {
				case <-credit:
					pending -= int(consumed.Swap(0))
				case <-ctx.Done():
					return
				}
			}
			if ctx.Err() != nil {
				return
			}
			n, err := process.Read(buffer)
			if n > 0 {
				write, done := context.WithTimeout(ctx, 10*time.Second)
				sendErr := c.Write(write, websocket.MessageBinary, buffer[:n])
				done()
				if sendErr != nil {
					return
				}
				pending += n
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		kind, raw, err := c.Read(ctx)
		if err != nil {
			return err
		}
		var msg terminalMessage
		if kind != websocket.MessageText || json.Unmarshal(raw, &msg) != nil {
			return errors.New("invalid terminal message")
		}
		switch msg.Type {
		case "input":
			if len(msg.Data) > 16384 {
				return errors.New("terminal input too large")
			}
			select {
			case input <- msg.Data:
			default:
				return errors.New("terminal input overflow")
			}
		case "resize":
			if !terminal.ValidSize(msg.Cols, msg.Rows) {
				return errors.New("invalid terminal size")
			}
			if err = process.Resize(msg.Cols, msg.Rows); err != nil {
				return err
			}
		case "ack":
			if msg.Bytes < 1 || msg.Bytes > 128*1024 {
				return errors.New("invalid terminal acknowledgment")
			}
			consumed.Add(int64(msg.Bytes))
			select {
			case credit <- struct{}{}:
			default:
			}
		default:
			return errors.New("invalid terminal message")
		}
	}
}
