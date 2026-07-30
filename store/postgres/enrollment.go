package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/store"
)

type enrollmentStore struct{ q querier }

const tokenColumns = `id, expires_at, consumed_at, device_id, created_at`

func (e enrollmentStore) Create(ctx context.Context, tokenHash string, expiresAt time.Time) (*store.EnrollmentToken, error) {
	const q = `
		INSERT INTO enrollment_tokens (token_hash, expires_at)
		VALUES ($1, $2)
		RETURNING ` + tokenColumns

	tok, err := scanToken(e.q.QueryRow(ctx, q, tokenHash, expiresAt))
	return tok, translate(err)
}

// Consume redeems a token, atomically.
//
// This is the single point that makes a photographed QR code worthless after
// first use, so it is one conditional UPDATE — not a SELECT followed by an
// UPDATE, which would let two phones both pass the check before either wrote.
// Postgres serializes the two updates on the row, so exactly one can match
// `consumed_at IS NULL` and the loser affects no rows.
//
// The follow-up SELECT runs only when nothing matched, purely to tell the three
// failures apart. It cannot turn a loser into a winner.
func (e enrollmentStore) Consume(ctx context.Context, tokenHash string, deviceID uuid.UUID, at time.Time) (*store.EnrollmentToken, error) {
	const q = `
		UPDATE enrollment_tokens
		SET consumed_at = $3, device_id = $2
		WHERE token_hash = $1 AND consumed_at IS NULL AND expires_at > $3
		RETURNING ` + tokenColumns

	tok, err := scanToken(e.q.QueryRow(ctx, q, tokenHash, deviceID, at))
	if err == nil {
		return tok, nil
	}
	if translated := translate(err); translated != store.ErrNotFound {
		return nil, translated
	}

	// Nothing was updated. Work out which of the three reasons it was, because
	// the operator's next move differs: mint another versus find out which
	// phone claimed the last one.
	var (
		expiresAt  time.Time
		consumedAt *time.Time
	)
	err = e.q.QueryRow(ctx,
		`SELECT expires_at, consumed_at FROM enrollment_tokens WHERE token_hash = $1`,
		tokenHash,
	).Scan(&expiresAt, &consumedAt)
	if err != nil {
		return nil, translate(err) // ErrNotFound: no such token
	}
	if consumedAt != nil {
		return nil, store.ErrConflict
	}
	return nil, store.ErrTokenExpired
}

// DeleteExpired sweeps tokens that ran out without being redeemed. Consumed
// ones are kept: they are the audit trail from "who minted a token" to "which
// phone joined".
func (e enrollmentStore) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	tag, err := e.q.Exec(ctx,
		`DELETE FROM enrollment_tokens WHERE consumed_at IS NULL AND expires_at < $1`,
		before)
	if err != nil {
		return 0, translate(err)
	}
	return tag.RowsAffected(), nil
}

func scanToken(row scanner) (*store.EnrollmentToken, error) {
	var t store.EnrollmentToken
	err := row.Scan(&t.ID, &t.ExpiresAt, &t.ConsumedAt, &t.DeviceID, &t.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
