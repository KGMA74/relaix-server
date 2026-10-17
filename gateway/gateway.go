// Package gateway runs a complete Relaix gateway: migrations, the device hub,
// the gRPC endpoint the phones hold open, the scheduler, callbacks and the
// REST API.
//
// gatewayd is Run with no extensions. A program embedding the gateway passes
// Extend to plug its own authentication, routing, admission rules and routes
// into the same process, without copying the wiring — and above all the start
// and stop order, which matters more than it looks: a scheduler that outlives
// the hub hands jobs to a registry that is gone, and an HTTP listener that
// outlives the scheduler accepts messages nothing will ever send.
package gateway

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
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
	"github.com/KGMA74/relaix-server/events"
	v1 "github.com/KGMA74/relaix-server/gen/smsgateway/v1"
	"github.com/KGMA74/relaix-server/grpcserver"
	"github.com/KGMA74/relaix-server/hub"
	"github.com/KGMA74/relaix-server/library"
	"github.com/KGMA74/relaix-server/mcp"
	"github.com/KGMA74/relaix-server/scheduler"
	"github.com/KGMA74/relaix-server/store"
	"github.com/KGMA74/relaix-server/store/postgres"
	"github.com/KGMA74/relaix-server/token"
	"github.com/KGMA74/relaix-server/ui"
)

// Env is what Extend gets to build its extensions from.
type Env struct {
	Config config.Config
	// Store is open and migrated. An extension with tables of its own reaches
	// the same database through Store.Querier.
	Store  *postgres.Store
	Logger *slog.Logger
}

// Extensions plug into the gateway. Every field is optional: left nil, the
// gateway behaves as a single-tenant gatewayd.
type Extensions struct {
	Authenticator     api.Authenticator
	Router            scheduler.Router
	SendGuard         api.SendGuard
	EnrollGuard       devices.Guard
	CallbackSecretFor func(ctx context.Context, account uuid.UUID) ([]byte, error)
	// InboundWebhookFor gives each account its own URL for received SMS.
	// Empty falls back to the configured one.
	InboundWebhookFor func(ctx context.Context, account uuid.UUID) (string, error)
	// OnInbound is told of each received SMS once recorded.
	OnInbound      func(ctx context.Context, m *store.InboundMessage)
	Routes         func(mux *http.ServeMux)
	PublicPrefixes []string
	// Library replaces the built-in catalogue of automation templates.
	Library library.Source
	// MCPTools are added to the MCP server's own tools.
	MCPTools []mcp.Tool
	UIConfig api.UIConfig
	// UI replaces the built-in dashboard.
	UI fs.FS
}

// Extend builds the extensions once the database is ready, before anything
// serves. It may run its own migrations.
type Extend func(ctx context.Context, env Env) (Extensions, error)

// Run serves until ctx is cancelled, then shuts down in order. extend may be
// nil.
func Run(ctx context.Context, cfg config.Config, logger *slog.Logger, extend Extend) error {
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

	var ext Extensions
	if extend != nil {
		if ext, err = extend(ctx, Env{Config: cfg, Store: st, Logger: logger}); err != nil {
			return fmt.Errorf("extend: %w", err)
		}
	}
	dashboard := ext.UI
	if dashboard == nil {
		dashboard = ui.FS()
	}

	// --- components --------------------------------------------------------

	hasher := token.SHA256{}

	// Live notifications for the dashboard: published by the gRPC handlers,
	// the scheduler and the API, read by GET /events.
	broker := events.New()

	h := hub.New(hub.Options{
		HeartbeatTTL: cfg.HeartbeatTTL,
		Logger:       logger,
	})

	enrollment := devices.New(st, hasher, devices.Options{
		TokenTTL:      cfg.EnrollTokenTTL,
		HardwareIDKey: []byte(cfg.HardwareIDKey),
		Guard:         ext.EnrollGuard,
		Logger:        logger,
	})
	if cfg.HardwareIDKey == "" {
		// Not fatal: one phone still maps to one device. But the stored
		// values are then a public-key HMAC, which a database dump could
		// match against hardware ids gathered elsewhere.
		logger.Warn("no hardware id key configured; phone identifiers are hashed with a public default key")
	}

	sched := scheduler.New(st, h, scheduler.Options{
		Interval:  cfg.SchedulerInterval,
		BatchSize: cfg.SchedulerBatch,
		Router:    ext.Router,
		Events:    broker,
		Logger:    logger,
	})

	notifier := callback.NewNotifier(callback.Options{
		Secret:    []byte(cfg.CallbackSecret),
		SecretFor: ext.CallbackSecretFor,
		Logger:    logger,
	})
	watcher := callback.NewWatcher(st, notifier, callback.WatcherOptions{
		Interval:    cfg.CallbackInterval,
		MaxAttempts: cfg.CallbackMaxAttempts,
		Logger:      logger,
	})
	inboundWatcher := callback.NewInboundWatcher(st, notifier, callback.WatcherOptions{
		Interval:    cfg.CallbackInterval,
		MaxAttempts: cfg.CallbackMaxAttempts,
		Logger:      logger,
	})
	inboundWebhook := func(ctx context.Context, account uuid.UUID) (string, error) {
		if ext.InboundWebhookFor != nil {
			url, err := ext.InboundWebhookFor(ctx, account)
			if err != nil || url != "" {
				return url, err
			}
		}
		return cfg.InboundWebhookURL, nil
	}
	if cfg.CallbackSecret == "" {
		// Not fatal: a deployment that never sets callbackUrl needs no secret.
		// But an unsigned callback is one the receiver cannot authenticate, so
		// it must not pass unnoticed.
		logger.Warn("no callback secret configured; outbound callbacks would be signed with an empty key")
	}

	deviceGateway := grpcserver.New(st, h, hasher, grpcserver.Options{
		HeartbeatInterval: cfg.HeartbeatInterval,
		Events:            broker,
		InboundWebhook:    inboundWebhook,
		OnInbound:         ext.OnInbound,
		Logger:            logger,
	}).WithEnroller(enrollment)

	grpcSrv, err := newGRPCServer(&cfg)
	if err != nil {
		return err
	}
	v1.RegisterDeviceGatewayServer(grpcSrv, deviceGateway)

	authenticator := ext.Authenticator
	if authenticator == nil {
		authenticator = api.SharedKey(cfg.APIKey)
	}
	httpSrv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: api.New(st, h, enrollment, api.Options{
			APIKey:         cfg.APIKey,
			Authenticator:  authenticator,
			Router:         ext.Router,
			SendGuard:      ext.SendGuard,
			Routes:         ext.Routes,
			PublicPrefixes: ext.PublicPrefixes,
			UIConfig:       ext.UIConfig,
			Library:        ext.Library,
			PublicURL:      cfg.PublicURL,
			UI:             dashboard,
			Events:         broker,
			Pinger:         st,
			Logger:         logger,
		}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// The MCP endpoint and its OAuth sit in front of the API: they check
	// credentials themselves, and every tool is a request to the API.
	// Without a secret of their own, the tokens are sealed with a key derived
	// from the API key, so assistants stay signed in across restarts.
	oauthSecret := cfg.OAuthSecret
	if oauthSecret == "" {
		oauthSecret = cfg.APIKey
	}
	if oauthSecret == "" {
		logger.Warn("no OAuth secret nor API key: assistants signed in by OAuth sign in again after a restart")
	}
	assistants, err := mcp.New(mcp.Options{
		API:           httpSrv.Handler,
		Authenticator: authenticator,
		Secret:        []byte(oauthSecret),
		Redirects:     cfg.OAuthRedirects,
		Name:          ext.UIConfig.Name,
		Tools:         ext.MCPTools,
		Logger:        logger,
	})
	if err != nil {
		return err
	}
	root := http.NewServeMux()
	assistants.Routes(root)
	root.Handle("/", httpSrv.Handler)
	httpSrv.Handler = root

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
	g.Go(func() error { return inboundWatcher.Run(gctx) })

	// --- stop --------------------------------------------------------------

	<-gctx.Done()

	// Deliberately not derived from gctx: that context is already cancelled, so
	// shutdown needs a fresh deadline of its own to do any work at all.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	logger.Info("gateway shutting down", "timeout", cfg.ShutdownTimeout)

	// Reverse order of dependency. HTTP first: stop accepting messages nothing
	// downstream will be around to send. Then gRPC gracefully, so devices are
	// told rather than dropped, and any in-flight result still lands. The
	// scheduler and watcher have already been asked to stop by gctx. The hub
	// goes last, because closing every outbound channel is how the remaining
	// stream handlers learn to end.
	// Event streams never end on their own, and Shutdown waits for every
	// active request: closing the broker is what lets them return.
	broker.Close()
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

	logger.Info("gateway stopped")
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
