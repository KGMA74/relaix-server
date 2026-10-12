// Command gatewayd is the Relaix control plane: it accepts gRPC streams from
// Android agents, tracks which devices are alive, and schedules SMS jobs onto
// them.
//
// The wiring lives in package gateway, which programs embedding the gateway
// run with their own extensions; gatewayd runs it with none.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/KGMA74/relaix-server/config"
	"github.com/KGMA74/relaix-server/gateway"
)

// version is overridden at build time:
//
//	go build -ldflags "-X main.version=$(git describe --tags --always)"
var version = "dev"

func main() {
	// `gatewayd -health-check` probes a running instance and exits 0 or 1.
	//
	// It lives in the binary because the runtime image is distroless: there is
	// no shell and no curl to write a container healthcheck with, and adding
	// either would mean shipping an attack surface purely so a probe can run.
	// Handled before config.Load because a probe has no database to be told
	// about.
	if len(os.Args) > 1 && os.Args[1] == "-health-check" {
		if err := healthCheck(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "gatewayd: health check:", err)
			os.Exit(1)
		}
		return
	}

	if err := run(os.Args[1:]); err != nil && !errors.Is(err, context.Canceled) {
		// The logger may not exist yet if configuration failed, so this goes to
		// stderr directly rather than through slog.
		fmt.Fprintln(os.Stderr, "gatewayd:", err)
		os.Exit(1)
	}
}

// run holds the lifecycle so that every path returns an error instead of
// calling os.Exit, which would skip deferred cleanup.
func run(args []string) error {
	cfg, err := config.Load(args)
	if err != nil {
		return err
	}

	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	// SIGINT and SIGTERM cancel the root context: Ctrl-C in a terminal, and
	// what a container runtime sends before it kills the process.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("gatewayd starting", "version", version, "pid", os.Getpid())
	logger.Info("configuration", "config", fmt.Sprintf("%+v", cfg.Redacted()))

	return gateway.Run(ctx, cfg, logger, nil)
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}

// healthCheck probes a running instance's readiness endpoint.
//
// It reads the address the same way the server does, so a deployment that moved
// the port does not have to remember to move it here too.
func healthCheck(args []string) error {
	fs := flag.NewFlagSet("gatewayd -health-check", flag.ContinueOnError)
	addr := fs.String("addr", "", "address to probe; defaults to RELAIX_HTTP_ADDR")
	if err := fs.Parse(args); err != nil {
		return err
	}

	target := *addr
	if target == "" {
		target = os.Getenv("RELAIX_HTTP_ADDR")
	}
	if target == "" {
		target = config.Default().HTTPAddr
	}
	// ":8080" is a listen address, not a URL: give it a host to dial.
	if strings.HasPrefix(target, ":") {
		target = "127.0.0.1" + target
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+target+"/readyz", nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("readyz returned %d", resp.StatusCode)
	}
	return nil
}
