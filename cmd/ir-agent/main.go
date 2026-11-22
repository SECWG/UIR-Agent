package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/SECWG/UIR-Agent/internal/agent"
	"github.com/SECWG/UIR-Agent/internal/state"
)

func main() {
	var o agent.Options
	flag.StringVar(&o.Server, "server", "", "platform HTTP/HTTPS origin")
	flag.StringVar(&o.JoinToken, "join-token", "", "short-lived join code, or - to read one line from stdin")
	flag.StringVar(&o.DataDir, "data-dir", "", "private agent state directory (reuse for reconnect)")
	flag.BoolVar(&o.Recover, "recover", false, "restore the original device using a device-bound recovery code")
	flag.StringVar(&o.Proxy, "proxy", "", "HTTP CONNECT proxy (defaults to HTTPS_PROXY / NO_PROXY)")
	relay := flag.Bool("relay", true, "allow platform-authorized SSH connection relay (use --relay=false to disable)")
	foreground := flag.Bool("foreground", false, "run in the current terminal instead of the default background mode")
	version := flag.Bool("version", false, "print version")
	flag.Parse()
	o.DisableRelay = !*relay
	if *version {
		fmt.Println(agent.Version)
		return
	}
	if o.JoinToken == "-" {
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			log.Fatal("join code missing from stdin")
		}
		o.JoinToken = strings.TrimSpace(scanner.Text())
	}
	if o.DataDir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			log.Fatal(err)
		}
		o.DataDir, err = state.DefaultDataDir(base, o.Server)
		if err != nil {
			log.Fatal(err)
		}
	}
	var err error
	o.DataDir, err = filepath.Abs(o.DataDir)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if os.Getenv(backgroundMarker) == "1" {
		_ = os.Unsetenv(backgroundMarker)
		err = serveBackground(ctx, o)
	} else if *foreground {
		err = agent.Serve(ctx, o)
	} else {
		err = startBackground(ctx, o)
	}
	var uninstall *agent.UninstallIntent
	if errors.As(err, &uninstall) {
		if err = agent.FinishUninstall(uninstall); err != nil {
			log.Printf("uninstall: %v", err)
		}
		return
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
