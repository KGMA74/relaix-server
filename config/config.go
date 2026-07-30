// Package config loads the server's configuration from flags and environment.
//
// Flags win over environment, environment wins over defaults. That order is the
// useful one: the environment is how a container is configured, and a flag is
// what someone types to override it for one run without editing the deployment.
package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the whole configuration of gatewayd.
type Config struct {
	// GRPCAddr is where devices connect.
	GRPCAddr string
	// HTTPAddr is where the REST API listens.
	HTTPAddr string

	// TLSCertFile and TLSKeyFile enable TLS on the gRPC listener. Both or
	// neither.
	TLSCertFile string
	TLSKeyFile  string

	// DatabaseURL is the Postgres DSN. Required.
	DatabaseURL string

	// APIKey guards the REST API. Empty disables authentication.
	APIKey string

	// CallbackSecret signs outbound webhooks. Required once anything uses
	// callbacks, and validated here rather than at the first delivery, because
	// discovering it at 3am from a failed callback is worse than at startup.
	CallbackSecret string

	// PublicURL is the address agents should dial, encoded into enrollment QR
	// codes.
	PublicURL string

	// EnrollTokenTTL is how long a minted enrollment token stays usable.
	EnrollTokenTTL time.Duration

	// HeartbeatInterval is the cadence handed to agents, and HeartbeatTTL is
	// how long a device stays ready without being heard from. The TTL must
	// exceed the interval or every device is stale the moment it connects.
	HeartbeatInterval time.Duration
	HeartbeatTTL      time.Duration

	// SchedulerInterval is the tick period, and SchedulerBatch bounds one tick.
	SchedulerInterval time.Duration
	SchedulerBatch    int

	// CallbackInterval is the watcher's poll period; the rest is its backoff.
	CallbackInterval    time.Duration
	CallbackMaxAttempts int

	// LogLevel is one of debug, info, warn, error.
	LogLevel string

	// ShutdownTimeout bounds the ordered stop.
	ShutdownTimeout time.Duration
}

// Default returns the configuration before flags and environment are applied.
func Default() Config {
	return Config{
		GRPCAddr:            ":9090",
		HTTPAddr:            ":8080",
		EnrollTokenTTL:      15 * time.Minute,
		HeartbeatInterval:   30 * time.Second,
		HeartbeatTTL:        90 * time.Second,
		SchedulerInterval:   time.Second,
		SchedulerBatch:      64,
		CallbackInterval:    5 * time.Second,
		CallbackMaxAttempts: 10,
		LogLevel:            "info",
		ShutdownTimeout:     15 * time.Second,
	}
}

// Load builds a Config from defaults, then RELAIX_* environment variables, then
// the given command-line arguments.
//
// It takes args rather than reading os.Args so it can be tested without a
// global flag set, and returns errors instead of exiting so a bad flag does not
// kill a process mid-test.
func Load(args []string) (Config, error) {
	cfg := Default()

	// Environment first, so flags can override it.
	if err := applyEnv(&cfg); err != nil {
		return cfg, err
	}

	fs := flag.NewFlagSet("gatewayd", flag.ContinueOnError)
	fs.StringVar(&cfg.GRPCAddr, "grpc-addr", cfg.GRPCAddr, "address for the device gRPC listener")
	fs.StringVar(&cfg.HTTPAddr, "http-addr", cfg.HTTPAddr, "address for the REST API")
	fs.StringVar(&cfg.TLSCertFile, "tls-cert", cfg.TLSCertFile, "TLS certificate for the gRPC listener")
	fs.StringVar(&cfg.TLSKeyFile, "tls-key", cfg.TLSKeyFile, "TLS key for the gRPC listener")
	fs.StringVar(&cfg.DatabaseURL, "database-url", cfg.DatabaseURL, "Postgres DSN")
	fs.StringVar(&cfg.APIKey, "api-key", cfg.APIKey, "bearer key for the REST API; empty disables auth")
	fs.StringVar(&cfg.CallbackSecret, "callback-secret", cfg.CallbackSecret, "HMAC secret for outbound callbacks")
	fs.StringVar(&cfg.PublicURL, "public-url", cfg.PublicURL, "address agents should dial, encoded into enrollment QR codes")
	fs.DurationVar(&cfg.EnrollTokenTTL, "enroll-token-ttl", cfg.EnrollTokenTTL, "lifetime of a minted enrollment token")
	fs.DurationVar(&cfg.HeartbeatInterval, "heartbeat-interval", cfg.HeartbeatInterval, "heartbeat cadence handed to agents")
	fs.DurationVar(&cfg.HeartbeatTTL, "heartbeat-ttl", cfg.HeartbeatTTL, "how long a device stays ready unheard from")
	fs.DurationVar(&cfg.SchedulerInterval, "scheduler-interval", cfg.SchedulerInterval, "scheduler tick period")
	fs.IntVar(&cfg.SchedulerBatch, "scheduler-batch", cfg.SchedulerBatch, "jobs claimed per tick")
	fs.DurationVar(&cfg.CallbackInterval, "callback-interval", cfg.CallbackInterval, "callback watcher poll period")
	fs.IntVar(&cfg.CallbackMaxAttempts, "callback-max-attempts", cfg.CallbackMaxAttempts, "attempts before a callback is abandoned")
	fs.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "debug, info, warn or error")
	fs.DurationVar(&cfg.ShutdownTimeout, "shutdown-timeout", cfg.ShutdownTimeout, "budget for the ordered shutdown")

	if err := fs.Parse(args); err != nil {
		return cfg, err
	}

	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// applyEnv reads RELAIX_* variables over the defaults.
func applyEnv(cfg *Config) error {
	str := func(key string, dst *string) {
		if v, ok := os.LookupEnv(key); ok {
			*dst = v
		}
	}
	str("RELAIX_GRPC_ADDR", &cfg.GRPCAddr)
	str("RELAIX_HTTP_ADDR", &cfg.HTTPAddr)
	str("RELAIX_TLS_CERT", &cfg.TLSCertFile)
	str("RELAIX_TLS_KEY", &cfg.TLSKeyFile)
	str("RELAIX_DATABASE_URL", &cfg.DatabaseURL)
	str("RELAIX_API_KEY", &cfg.APIKey)
	str("RELAIX_CALLBACK_SECRET", &cfg.CallbackSecret)
	str("RELAIX_PUBLIC_URL", &cfg.PublicURL)
	str("RELAIX_LOG_LEVEL", &cfg.LogLevel)

	// DATABASE_URL without the prefix is what nearly every hosting platform
	// injects, so it is accepted as a fallback rather than making every
	// deployment rename it.
	if cfg.DatabaseURL == "" {
		str("DATABASE_URL", &cfg.DatabaseURL)
	}

	durations := map[string]*time.Duration{
		"RELAIX_ENROLL_TOKEN_TTL":   &cfg.EnrollTokenTTL,
		"RELAIX_HEARTBEAT_INTERVAL": &cfg.HeartbeatInterval,
		"RELAIX_HEARTBEAT_TTL":      &cfg.HeartbeatTTL,
		"RELAIX_SCHEDULER_INTERVAL": &cfg.SchedulerInterval,
		"RELAIX_CALLBACK_INTERVAL":  &cfg.CallbackInterval,
		"RELAIX_SHUTDOWN_TIMEOUT":   &cfg.ShutdownTimeout,
	}
	for key, dst := range durations {
		v, ok := os.LookupEnv(key)
		if !ok {
			continue
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("config: %s: %w", key, err)
		}
		*dst = d
	}

	ints := map[string]*int{
		"RELAIX_SCHEDULER_BATCH":       &cfg.SchedulerBatch,
		"RELAIX_CALLBACK_MAX_ATTEMPTS": &cfg.CallbackMaxAttempts,
	}
	for key, dst := range ints {
		v, ok := os.LookupEnv(key)
		if !ok {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("config: %s: %w", key, err)
		}
		*dst = n
	}

	return nil
}

// Validate rejects a configuration that cannot work.
//
// Everything checked here is something that would otherwise surface much later
// and much less legibly — a TLS key missing its certificate at the first
// connection, an unsigned callback at the first delivery, a heartbeat TTL
// shorter than the interval quietly making every device unschedulable.
func (c *Config) Validate() error {
	var problems []string

	if c.DatabaseURL == "" {
		problems = append(problems, "database URL is required (-database-url or RELAIX_DATABASE_URL)")
	}
	if c.GRPCAddr == "" {
		problems = append(problems, "grpc address is required")
	}
	if c.HTTPAddr == "" {
		problems = append(problems, "http address is required")
	}

	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		problems = append(problems, "TLS needs both a certificate and a key, or neither")
	}

	// A device that has to heartbeat every 30s but goes stale after 10s is
	// never ready, and nothing about the symptom points at the cause.
	if c.HeartbeatTTL <= c.HeartbeatInterval {
		problems = append(problems, fmt.Sprintf(
			"heartbeat TTL (%s) must exceed the heartbeat interval (%s), or every device is stale on arrival",
			c.HeartbeatTTL, c.HeartbeatInterval))
	}

	if c.HeartbeatInterval <= 0 {
		problems = append(problems, "heartbeat interval must be positive")
	}
	if c.SchedulerInterval <= 0 {
		problems = append(problems, "scheduler interval must be positive")
	}
	if c.SchedulerBatch <= 0 {
		problems = append(problems, "scheduler batch must be positive")
	}
	if c.CallbackInterval <= 0 {
		problems = append(problems, "callback interval must be positive")
	}
	if c.CallbackMaxAttempts <= 0 {
		problems = append(problems, "callback max attempts must be positive")
	}
	if c.EnrollTokenTTL <= 0 {
		problems = append(problems, "enrollment token TTL must be positive")
	}
	if c.ShutdownTimeout <= 0 {
		problems = append(problems, "shutdown timeout must be positive")
	}

	switch strings.ToLower(c.LogLevel) {
	case "debug", "info", "warn", "error":
	default:
		problems = append(problems, fmt.Sprintf("unknown log level %q", c.LogLevel))
	}

	if len(problems) == 0 {
		return nil
	}
	return errors.New("config:\n  - " + strings.Join(problems, "\n  - "))
}

// TLSEnabled reports whether the gRPC listener should use TLS.
func (c *Config) TLSEnabled() bool {
	return c.TLSCertFile != "" && c.TLSKeyFile != ""
}

// Redacted returns the configuration with secrets removed, for logging at
// startup. Logging the config is genuinely useful for debugging a deployment;
// logging the API key with it is how a secret ends up in a log aggregator.
func (c *Config) Redacted() Config {
	out := *c
	out.APIKey = redact(c.APIKey)
	out.CallbackSecret = redact(c.CallbackSecret)
	out.DatabaseURL = redactDSN(c.DatabaseURL)
	return out
}

func redact(s string) string {
	if s == "" {
		return ""
	}
	return "[redacted]"
}

// redactDSN keeps the DSN readable while removing the password, which is the
// part that matters and the part people forget is in there.
func redactDSN(dsn string) string {
	if dsn == "" {
		return ""
	}
	scheme, rest, ok := strings.Cut(dsn, "://")
	if !ok {
		return "[redacted]"
	}
	creds, host, ok := strings.Cut(rest, "@")
	if !ok {
		return dsn // no credentials to hide
	}
	user, _, hasPassword := strings.Cut(creds, ":")
	if !hasPassword {
		return dsn
	}
	return scheme + "://" + user + ":[redacted]@" + host
}
