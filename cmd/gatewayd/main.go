// Command gatewayd is the Relaix control plane: it accepts gRPC streams from
// Android agents, tracks which devices are alive, and schedules SMS jobs onto
// them.
//
// It does none of that yet. This is the entry point and the process lifecycle
// around it — startup, signal handling, ordered shutdown — with the components
// still to come. Each one is wired in as it lands, rather than the whole thing
// appearing at once in a single unreviewable commit.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// version is overridden at build time:
//
//	go build -ldflags "-X main.version=$(git describe --tags --always)"
var version = "dev"

// shutdownTimeout bounds the ordered stop. Once components exist, this is the
// budget for draining in-flight work — a job already pushed to a handset should
// get its result recorded rather than being lost to an abrupt exit.
const shutdownTimeout = 15 * time.Second

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("gatewayd exited with error", "err", err)
		os.Exit(1)
	}
}

// run holds the lifecycle so that every path returns an error instead of
// calling os.Exit, which would skip deferred cleanup.
func run() error {
	// SIGINT and SIGTERM cancel the root context: Ctrl-C in a terminal, and
	// what a container runtime sends before it kills the process.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	slog.Info("gatewayd starting", "version", version, "pid", os.Getpid())

	// Components are started here as they are built: config, store, hub,
	// scheduler, gRPC server, HTTP API, callback watcher. Until then there is
	// nothing to serve, so the process is honest about it and waits for a
	// signal rather than pretending to be a running gateway.
	slog.Warn("no components wired yet; gatewayd does not serve anything")

	<-ctx.Done()

	// Deliberately not derived from ctx: that context is already cancelled by
	// the signal, so shutdown needs a fresh deadline of its own to do any work.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	slog.Info("gatewayd shutting down", "timeout", shutdownTimeout)
	if err := shutdown(shutdownCtx); err != nil {
		return err
	}

	slog.Info("gatewayd stopped")
	return nil
}

// shutdown stops the running components in reverse order of startup, so that
// nothing accepts new work while something it depends on is already gone.
func shutdown(ctx context.Context) error {
	_ = ctx // nothing to stop yet
	return nil
}
