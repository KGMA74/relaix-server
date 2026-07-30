// Command gatewayd is the Relaix control plane: it accepts gRPC streams from
// Android agents, tracks which devices are alive, and schedules SMS jobs onto
// them.
//
// This file is only wiring. Every decision it depends on lives in the package
// it configures; what is decided here is the order things start and stop in,
// which matters more than it looks: a scheduler that outlives the hub hands
// jobs to a registry that is gone, and an HTTP listener that outlives the
// scheduler accepts messages nothing will ever send.
package main

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/KGMA74/relaix-server/api"
	"github.com/KGMA74/relaix-server/callback"
	"github.com/KGMA74/relaix-server/config"
	"github.com/KGMA74/relaix-server/db"
	"github.com/KGMA74/relaix-server/devices"
	v1 "github.com/KGMA74/relaix-server/gen/smsgateway/v1"
	"github.com/KGMA74/relaix-server/grpcserver"
	"github.com/KGMA74/relaix-server/hub"
	"github.com/KGMA74/relaix-server/scheduler"
	"github.com/KGMA74/relaix-server/store/postgres"
	"github.com/KGMA74/relaix-server/token"
)

// version is overridden at build time:
//
//	go build -ldflags "-X main.version=$(git describe --tags --always)"
var version = "dev"

func main() {
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

	// --- persistence -------------------------------------------------------

	if err := migrate(ctx, cfg.DatabaseURL, logger); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	st, err := postgres.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()
	logger.Info("connected to postgres")

	// --- components --------------------------------------------------------

	hasher := token.SHA256{}

	h := hub.New(hub.Options{
		HeartbeatTTL: cfg.HeartbeatTTL,
		Logger:       logger,
	})

	enrollment := devices.New(st, hasher, devices.Options{
		TokenTTL: cfg.EnrollTokenTTL,
		Logger:   logger,
	})

	sched := scheduler.New(st, h, scheduler.Options{
		Interval:  cfg.SchedulerInterval,
		BatchSize: cfg.SchedulerBatch,
		Logger:    logger,
	})

	notifier := callback.NewNotifier(callback.Options{
		Secret: []byte(cfg.CallbackSecret),
		Logger: logger,
	})
	watcher := callback.NewWatcher(st, notifier, callback.WatcherOptions{
		Interval:    cfg.CallbackInterval,
		MaxAttempts: cfg.CallbackMaxAttempts,
		Logger:      logger,
	})
	if cfg.CallbackSecret == "" {
		// Not fatal: a deployment that never sets callbackUrl needs no secret.
		// But an unsigned callback is one the receiver cannot authenticate, so
		// it must not pass unnoticed.
		logger.Warn("no callback secret configured; outbound callbacks would be signed with an empty key")
	}

	deviceGateway := grpcserver.New(st, h, hasher, grpcserver.Options{
		HeartbeatInterval: cfg.HeartbeatInterval,
		Logger:            logger,
	}).WithEnroller(enrollment)

	grpcSrv, err := newGRPCServer(&cfg)
	if err != nil {
		return err
	}
	v1.RegisterDeviceGatewayServer(grpcSrv, deviceGateway)

	httpSrv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: api.New(st, h, enrollment, api.Options{
			APIKey:    cfg.APIKey,
			PublicURL: cfg.PublicURL,
			Logger:    logger,
		}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// --- start -------------------------------------------------------------

	// The hub runs in its own context so it can be stopped last: the gRPC
	// handlers and the scheduler both talk to it, and stopping it first would
	// have them fail on the way out.
	hubCtx, stopHub := context.WithCancel(context.Background())
	defer stopHub()

	hubDone := make(chan struct{})
	go func() {
		defer close(hubDone)
		_ = h.Run(hubCtx)
	}()

	grpcLis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen grpc: %w", err)
	}

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		logger.Info("grpc listening", "addr", cfg.GRPCAddr, "tls", cfg.TLSEnabled())
		if err := grpcSrv.Serve(grpcLis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			return fmt.Errorf("grpc serve: %w", err)
		}
		return nil
	})

	g.Go(func() error {
		logger.Info("http listening", "addr", cfg.HTTPAddr, "auth", cfg.APIKey != "")
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http serve: %w", err)
		}
		return nil
	})

	g.Go(func() error { return sched.Run(gctx) })
	g.Go(func() error { return watcher.Run(gctx) })

	// --- stop --------------------------------------------------------------

	<-gctx.Done()

	// Deliberately not derived from gctx: that context is already cancelled, so
	// shutdown needs a fresh deadline of its own to do any work at all.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	logger.Info("gatewayd shutting down", "timeout", cfg.ShutdownTimeout)

	// Reverse order of dependency. HTTP first: stop accepting messages nothing
	// downstream will be around to send. Then gRPC gracefully, so devices are
	// told rather than dropped, and any in-flight result still lands. The
	// scheduler and watcher have already been asked to stop by gctx. The hub
	// goes last, because closing every outbound channel is how the remaining
	// stream handlers learn to end.
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("http shutdown", "err", err)
	}

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		grpcSrv.GracefulStop()
	}()
	select {
	case <-stopped:
	case <-shutdownCtx.Done():
		// A device holding an open stream will not close on its own; past the
		// budget, take it away rather than hang forever.
		logger.Warn("grpc did not stop gracefully in time, forcing")
		grpcSrv.Stop()
		<-stopped
	}

	stopHub()
	<-hubDone

	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}

	logger.Info("gatewayd stopped")
	return nil
}

// newGRPCServer builds the server, with TLS when configured.
func newGRPCServer(cfg *config.Config) (*grpc.Server, error) {
	if !cfg.TLSEnabled() {
		// Devices authenticate the server by TLS, so running without it means
		// device tokens cross the network in the clear. Acceptable only behind
		// a terminating proxy, and said out loud either way.
		slog.Warn("gRPC is serving without TLS; device tokens will not be encrypted in transit")
		return grpc.NewServer(), nil
	}

	cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load tls keypair: %w", err)
	}
	creds := credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	})
	return grpc.NewServer(grpc.Creds(creds)), nil
}

// migrate brings the schema up to date from the embedded migrations, so a fresh
// deployment works with nothing but a database URL and no separate step to
// forget.
//
// goose needs a database/sql handle, which pgx provides through its stdlib
// adapter; the pool used for everything else is opened afterwards.
func migrate(ctx context.Context, dsn string, logger *slog.Logger) error {
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer sqlDB.Close()

	goose.SetBaseFS(db.Migrations)
	goose.SetLogger(gooseLogger{logger})
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}

	if err := goose.UpContext(ctx, sqlDB, "migrations"); err != nil {
		return err
	}

	schemaVersion, err := goose.GetDBVersionContext(ctx, sqlDB)
	if err != nil {
		return err
	}
	logger.Info("database schema up to date", "version", schemaVersion)
	return nil
}

// gooseLogger adapts goose's logging onto slog, so migration output lands in
// the same stream as everything else rather than bare on stdout.
type gooseLogger struct{ log *slog.Logger }

func (g gooseLogger) Fatalf(format string, v ...any) {
	g.log.Error("goose: " + strings.TrimSpace(fmt.Sprintf(format, v...)))
}

func (g gooseLogger) Printf(format string, v ...any) {
	g.log.Info("goose: " + strings.TrimSpace(fmt.Sprintf(format, v...)))
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
