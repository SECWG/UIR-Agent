//go:build !windows

package management

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/SECWG/UIR-Agent/internal/execution"
	"github.com/SECWG/UIR-Agent/internal/terminal"
)

//go:embed host-info.sh
var hostInfoUnix string

type hostInfoBuffer struct {
	bytes.Buffer
	cancel   context.CancelFunc
	overflow bool
}

func (b *hostInfoBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > hostInfoLimit {
		b.overflow = true
		b.cancel()
		return 0, errors.New("HOST_INFO_TOO_LARGE")
	}
	return b.Buffer.Write(p)
}

func collectHostInformation(parent context.Context) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(parent, 12*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return nil, errors.New("HOST_INFO_TIMEOUT")
	}
	cmd := exec.Command("/bin/sh", "-s")
	cmd.Stdin = strings.NewReader(strings.ReplaceAll(hostInfoUnix, "\r\n", "\n"))
	output := &hostInfoBuffer{cancel: cancel}
	cmd.Env, cmd.Stdout, cmd.Stderr = terminal.Environment(), output, io.Discard
	cmd.WaitDelay = time.Second
	kill, cleanup, err := execution.StartProcess(cmd)
	if err != nil {
		return nil, errors.New("HOST_INFO_UNAVAILABLE")
	}
	defer cleanup()
	killed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		kill()
		close(killed)
	})
	defer func() {
		if !stop() {
			<-killed
		}
	}()
	err = cmd.Wait()
	if output.overflow {
		return nil, errors.New("HOST_INFO_INVALID")
	}
	if ctx.Err() != nil {
		return nil, errors.New("HOST_INFO_TIMEOUT")
	}
	if err != nil {
		return nil, errors.New("HOST_INFO_UNAVAILABLE")
	}
	return parseHostInformation(output.Bytes())
}
