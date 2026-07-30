package config

import (
	"strings"
	"testing"
	"time"
)

// validArgs is the minimum that loads.
func validArgs(extra ...string) []string {
	return append([]string{"-database-url", "postgres://u:p@localhost/relaix"}, extra...)
}

func TestLoadUsesDefaults(t *testing.T) {
	cfg, err := Load(validArgs())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := Default()
	if cfg.GRPCAddr != want.GRPCAddr || cfg.HTTPAddr != want.HTTPAddr {
		t.Errorf("addresses = %q/%q, want %q/%q",
			cfg.GRPCAddr, cfg.HTTPAddr, want.GRPCAddr, want.HTTPAddr)
	}
	if cfg.SchedulerInterval != want.SchedulerInterval {
		t.Errorf("scheduler interval = %v, want %v", cfg.SchedulerInterval, want.SchedulerInterval)
	}
}

func TestEnvIsRead(t *testing.T) {
	t.Setenv("RELAIX_DATABASE_URL", "postgres://env/db")
	t.Setenv("RELAIX_HTTP_ADDR", ":9999")
	t.Setenv("RELAIX_SCHEDULER_INTERVAL", "2s")
	t.Setenv("RELAIX_SCHEDULER_BATCH", "7")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DatabaseURL != "postgres://env/db" {
		t.Errorf("database url = %q", cfg.DatabaseURL)
	}
	if cfg.HTTPAddr != ":9999" {
		t.Errorf("http addr = %q", cfg.HTTPAddr)
	}
	if cfg.SchedulerInterval != 2*time.Second {
		t.Errorf("scheduler interval = %v", cfg.SchedulerInterval)
	}
	if cfg.SchedulerBatch != 7 {
		t.Errorf("scheduler batch = %d", cfg.SchedulerBatch)
	}
}

// A flag is what someone types to override the deployment for one run, so it
// has to win.
func TestFlagsOverrideEnv(t *testing.T) {
	t.Setenv("RELAIX_DATABASE_URL", "postgres://env/db")
	t.Setenv("RELAIX_HTTP_ADDR", ":1111")

	cfg, err := Load([]string{"-http-addr", ":2222"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTPAddr != ":2222" {
		t.Errorf("http addr = %q, want the flag value", cfg.HTTPAddr)
	}
	// The environment still supplies what the flags did not.
	if cfg.DatabaseURL != "postgres://env/db" {
		t.Errorf("database url = %q", cfg.DatabaseURL)
	}
}

// Hosting platforms inject DATABASE_URL unprefixed; making every deployment
// rename it would be gratuitous.
func TestBareDatabaseURLIsAccepted(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://platform/db")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DatabaseURL != "postgres://platform/db" {
		t.Errorf("database url = %q", cfg.DatabaseURL)
	}
}

func TestPrefixedDatabaseURLWins(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://platform/db")
	t.Setenv("RELAIX_DATABASE_URL", "postgres://explicit/db")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DatabaseURL != "postgres://explicit/db" {
		t.Errorf("database url = %q, want the RELAIX_ one", cfg.DatabaseURL)
	}
}

func TestMissingDatabaseURLIsRejected(t *testing.T) {
	_, err := Load(nil)
	if err == nil {
		t.Fatal("Load accepted a configuration with no database")
	}
	if !strings.Contains(err.Error(), "database URL") {
		t.Errorf("error does not name the problem: %v", err)
	}
}

func TestValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantSub string
	}{
		{
			name:    "tls cert without key",
			mutate:  func(c *Config) { c.TLSCertFile = "cert.pem" },
			wantSub: "TLS needs both",
		},
		{
			name:    "tls key without cert",
			mutate:  func(c *Config) { c.TLSKeyFile = "key.pem" },
			wantSub: "TLS needs both",
		},
		{
			// A device that heartbeats every 30s but goes stale after 10s is
			// never ready, and the symptom points nowhere near the cause.
			name: "heartbeat ttl below interval",
			mutate: func(c *Config) {
				c.HeartbeatInterval = 30 * time.Second
				c.HeartbeatTTL = 10 * time.Second
			},
			wantSub: "must exceed the heartbeat interval",
		},
		{
			name: "heartbeat ttl equal to interval",
			mutate: func(c *Config) {
				c.HeartbeatInterval = 30 * time.Second
				c.HeartbeatTTL = 30 * time.Second
			},
			wantSub: "must exceed the heartbeat interval",
		},
		{
			name:    "zero scheduler interval",
			mutate:  func(c *Config) { c.SchedulerInterval = 0 },
			wantSub: "scheduler interval",
		},
		{
			name:    "negative scheduler batch",
			mutate:  func(c *Config) { c.SchedulerBatch = -1 },
			wantSub: "scheduler batch",
		},
		{
			name:    "zero callback attempts",
			mutate:  func(c *Config) { c.CallbackMaxAttempts = 0 },
			wantSub: "callback max attempts",
		},
		{
			name:    "unknown log level",
			mutate:  func(c *Config) { c.LogLevel = "verbose" },
			wantSub: "log level",
		},
		{
			name:    "no http address",
			mutate:  func(c *Config) { c.HTTPAddr = "" },
			wantSub: "http address",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			cfg.DatabaseURL = "postgres://u:p@localhost/relaix"
			tc.mutate(&cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate accepted an unusable configuration")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not mention %q", err, tc.wantSub)
			}
		})
	}
}

// All the problems at once, so a misconfigured deployment is fixed in one pass
// rather than one restart per mistake.
func TestValidateReportsEveryProblem(t *testing.T) {
	cfg := Default()
	cfg.DatabaseURL = ""
	cfg.LogLevel = "loud"
	cfg.SchedulerBatch = 0

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate accepted an unusable configuration")
	}
	for _, want := range []string{"database URL", "log level", "scheduler batch"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

func TestTLSEnabled(t *testing.T) {
	cfg := Default()
	if cfg.TLSEnabled() {
		t.Error("TLS reported enabled with no certificate")
	}
	cfg.TLSCertFile, cfg.TLSKeyFile = "c", "k"
	if !cfg.TLSEnabled() {
		t.Error("TLS reported disabled with both files set")
	}
}

// Logging the config at startup is useful; logging the secrets with it is how
// they end up in a log aggregator.
func TestRedactedHidesSecrets(t *testing.T) {
	cfg := Default()
	cfg.APIKey = "super-secret-key"
	cfg.CallbackSecret = "hmac-secret"
	cfg.DatabaseURL = "postgres://relaix:hunter2@db.internal:5432/relaix?sslmode=require"

	got := cfg.Redacted()

	for _, secret := range []string{"super-secret-key", "hmac-secret", "hunter2"} {
		if strings.Contains(got.APIKey+got.CallbackSecret+got.DatabaseURL, secret) {
			t.Errorf("%q survived redaction", secret)
		}
	}
	// Still readable enough to debug with.
	if !strings.Contains(got.DatabaseURL, "db.internal:5432") {
		t.Errorf("redacted DSN lost the host: %q", got.DatabaseURL)
	}
	if !strings.Contains(got.DatabaseURL, "relaix:") {
		t.Errorf("redacted DSN lost the user: %q", got.DatabaseURL)
	}
	// And the original is untouched.
	if cfg.APIKey != "super-secret-key" {
		t.Error("Redacted mutated the receiver")
	}
}

func TestRedactedHandlesDSNsWithoutCredentials(t *testing.T) {
	cfg := Default()
	cfg.DatabaseURL = "postgres://localhost:5432/relaix"

	if got := cfg.Redacted().DatabaseURL; got != cfg.DatabaseURL {
		t.Errorf("DSN without credentials was altered: %q", got)
	}
}

func TestBadDurationInEnvIsReported(t *testing.T) {
	t.Setenv("RELAIX_DATABASE_URL", "postgres://env/db")
	t.Setenv("RELAIX_SCHEDULER_INTERVAL", "soon")

	_, err := Load(nil)
	if err == nil {
		t.Fatal("Load accepted an unparseable duration")
	}
	if !strings.Contains(err.Error(), "RELAIX_SCHEDULER_INTERVAL") {
		t.Errorf("error does not name the variable: %v", err)
	}
}

func TestBadIntInEnvIsReported(t *testing.T) {
	t.Setenv("RELAIX_DATABASE_URL", "postgres://env/db")
	t.Setenv("RELAIX_SCHEDULER_BATCH", "many")

	_, err := Load(nil)
	if err == nil {
		t.Fatal("Load accepted an unparseable integer")
	}
	if !strings.Contains(err.Error(), "RELAIX_SCHEDULER_BATCH") {
		t.Errorf("error does not name the variable: %v", err)
	}
}

// A bad flag must return an error, not kill the process.
func TestUnknownFlagIsAnError(t *testing.T) {
	if _, err := Load([]string{"-nonsense"}); err == nil {
		t.Fatal("Load accepted an unknown flag")
	}
}
