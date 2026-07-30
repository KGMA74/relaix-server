// Package postgres implements store.Store on Postgres with pgx.
//
// The shape follows the interfaces rather than the tables: the four sub-stores
// are separate types over a shared querier, which is either the connection pool
// or an open transaction. That is what lets WithTx hand the callback a Store
// whose every write lands in the same transaction, without any caller having to
// thread a transaction handle through its own signatures.
//
// Schema: db/migrations/00001_init.sql.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KGMA74/relaix-server/store"
)

// querier is the subset of pgx shared by *pgxpool.Pool and pgx.Tx. Every query
// in this package goes through it, so the same code serves both.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store is a store.Store backed by Postgres.
type Store struct {
	pool *pgxpool.Pool
	q    querier
}

// New connects to Postgres and verifies the connection before returning, so a
// bad DSN fails at startup rather than on the first request.
func New(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	return &Store{pool: pool, q: pool}, nil
}

// NewWithPool wraps an existing pool.
func NewWithPool(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, q: pool}
}

// Close releases the pool. Safe to call on a Store handed to WithTx, where it
// is a no-op because that Store does not own the pool.
func (s *Store) Close() {
	if s.pool != nil {
		s.pool.Close()
	}
}

// Devices implements store.Store.
func (s *Store) Devices() store.DeviceStore { return deviceStore{s.q} }

// Jobs implements store.Store.
func (s *Store) Jobs() store.JobStore { return jobStore{s.q} }

// Enrollments implements store.Store.
func (s *Store) Enrollments() store.EnrollmentStore { return enrollmentStore{s.q} }

// Events implements store.Store.
func (s *Store) Events() store.EventStore { return eventStore{s.q} }

// WithTx runs fn in a transaction, rolling back on error or panic.
//
// The rollback is deferred rather than only run on the error path so that a
// panic inside fn cannot leave a transaction open holding row locks — which,
// given ClaimSchedulable takes FOR UPDATE, would stall every other scheduler
// until the connection died.
func (s *Store) WithTx(ctx context.Context, fn func(store.Store) error) error {
	if s.pool == nil {
		return errors.New("postgres: nested WithTx")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// pool is nil in the scoped Store: it must not be able to open a nested
	// transaction, and it must not close the pool it does not own.
	if err := fn(&Store{q: tx}); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit: %w", err)
	}
	return nil
}

// Compile-time proof that this implements the boundary it stands behind.
var (
	_ store.Store           = (*Store)(nil)
	_ store.DeviceStore     = deviceStore{}
	_ store.JobStore        = jobStore{}
	_ store.EnrollmentStore = enrollmentStore{}
	_ store.EventStore      = eventStore{}
)

// translate maps driver errors onto the store's vocabulary, so nothing above
// this package ever sees a pgx or Postgres error.
func translate(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return store.ErrConflict
		case "23503": // foreign_key_violation
			return store.ErrConflict
		case "23514": // check_violation — a value the schema refuses
			return fmt.Errorf("%w: %s", store.ErrConflict, pgErr.ConstraintName)
		}
	}
	return err
}

// Ping reports whether the database answers. Used by the readiness probe.
func (s *Store) Ping(ctx context.Context) error {
	if s.pool == nil {
		return errors.New("postgres: no pool on a transaction-scoped store")
	}
	return s.pool.Ping(ctx)
}
