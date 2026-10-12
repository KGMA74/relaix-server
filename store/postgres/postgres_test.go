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

// What an embedding program writes through Querier inside WithTx commits or
// rolls back with the gateway's own writes.
func TestQuerierFollowsTheTransaction(t *testing.T) {
	dsn := os.Getenv(envDSN)
	if dsn == "" {
		t.Skipf("%s is not set; run `make test-integration`", envDSN)
	}
	ctx := context.Background()
	st, err := postgres.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(st.Close)

	count := func(name string) int {
		var n int
		if err := st.Querier().QueryRow(ctx, `SELECT count(*) FROM accounts WHERE name = $1`, name).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}
	write := func(name string, fail bool) {
		_ = st.WithTx(ctx, func(tx store.Store) error {
			if _, err := tx.(*postgres.Store).Querier().Exec(ctx, `INSERT INTO accounts (name) VALUES ($1)`, name); err != nil {
				t.Fatalf("insert: %v", err)
			}
			if fail {
				return fmt.Errorf("roll back")
			}
			return nil
		})
	}

	rolled, kept := "querier-rolled-back", "querier-kept"
	write(rolled, true)
	write(kept, false)
	if count(rolled) != 0 || count(kept) != 1 {
		t.Errorf("rolled back %d, kept %d; want 0 and 1", count(rolled), count(kept))
	}
	if _, err := st.Querier().Exec(ctx, `DELETE FROM accounts WHERE name = $1`, kept); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}
