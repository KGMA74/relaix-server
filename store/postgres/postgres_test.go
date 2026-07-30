package postgres_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KGMA74/relaix-server/store"
	"github.com/KGMA74/relaix-server/store/postgres"
	"github.com/KGMA74/relaix-server/store/storetest"
)

// envDSN points at a migrated database. The suite is skipped without it, so
// `go test ./...` stays runnable with no infrastructure; `make test-integration`
// sets it up and sets the variable.
const envDSN = "RELAIX_TEST_DATABASE_URL"

// TestPostgresConformance runs the same contract the in-memory fake is held to.
//
// This is the point of the shared suite: the rest of the test suite trusts the
// fake, and that trust is only warranted if both implementations answer the
// same way. A rule Postgres enforces that the fake does not — or a behaviour
// the fake invents — shows up here rather than in production.
func TestPostgresConformance(t *testing.T) {
	dsn := os.Getenv(envDSN)
	if dsn == "" {
		t.Skipf("%s is not set; run `make test-integration`", envDSN)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	storetest.RunConformance(t, func(t *testing.T) store.Store {
		truncate(t, pool)
		return postgres.NewWithPool(pool)
	})
}

// truncate empties every table between subtests. The conformance suite assumes
// it starts from nothing, and CASCADE is needed because job_events references
// jobs.
func truncate(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	const q = `TRUNCATE job_events, jobs, enrollment_tokens, devices RESTART IDENTITY CASCADE`
	if _, err := pool.Exec(context.Background(), q); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func TestNewRejectsABadDSN(t *testing.T) {
	// A bad DSN must fail at startup rather than on the first request, so this
	// asserts New actually reaches the server instead of just parsing.
	_, err := postgres.New(context.Background(),
		"postgres://nobody:nobody@127.0.0.1:1/nothing?sslmode=disable&connect_timeout=1")
	if err == nil {
		t.Fatal("New accepted a DSN pointing at nothing")
	}
	fmt.Fprintln(os.Stderr, "expected failure:", err)
}
