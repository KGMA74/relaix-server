package postgres

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/store"
)

type inboundStore struct{ q querier }

const inboundColumns = `
	id, account_id, device_id, device_ref, subscription_id, sender, body,
	received_at, created_at, automation_id, fields,
	webhook_url, webhook_attempts, webhook_next_at, webhook_delivered_at, webhook_last_error`

// Record leans on the unique index over (device_id, device_ref): a resend
// inserts nothing, and the stored row is read back instead.
func (s inboundStore) Record(ctx context.Context, in *store.InboundMessage) (*store.InboundMessage, bool, error) {
	const q = `
		INSERT INTO inbound_messages (
			account_id, device_id, device_ref, subscription_id, sender, body, received_at,
			webhook_url, webhook_next_at, automation_id, fields
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (device_id, device_ref) DO NOTHING
		RETURNING ` + inboundColumns

	m, err := scanInbound(s.q.QueryRow(ctx, q,
		store.AccountOrDefault(in.AccountID), in.DeviceID, in.DeviceRef, in.SubscriptionID,
		in.Sender, in.Body, in.ReceivedAt, in.Webhook.URL, in.Webhook.NextAttemptAt,
		in.AutomationID, fieldsOrEmpty(in.Fields),
	))
	if err == nil {
		return m, true, nil
	}
	if err = translate(err); !errors.Is(err, store.ErrNotFound) {
		return nil, false, err
	}
	existing, err := scanInbound(s.q.QueryRow(ctx,
		`SELECT `+inboundColumns+` FROM inbound_messages WHERE device_id = $1 AND device_ref = $2`,
		in.DeviceID, in.DeviceRef))
	return existing, false, translate(err)
}

func (s inboundStore) Get(ctx context.Context, id uuid.UUID) (*store.InboundMessage, error) {
	m, err := scanInbound(s.q.QueryRow(ctx,
		`SELECT `+inboundColumns+` FROM inbound_messages WHERE id = $1`, id))
	return m, translate(err)
}

// List follows jobStore.List: positional arguments only, and a row-comparison
// cursor matching the ORDER BY, served by inbound_messages_account_received_idx.
func (s inboundStore) List(ctx context.Context, f store.InboundFilter) ([]*store.InboundMessage, error) {
	var (
		where []string
		args  []any
	)
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if f.AccountID != nil {
		where = append(where, "account_id = "+arg(*f.AccountID))
	}
	if f.DeviceID != nil {
		where = append(where, "device_id = "+arg(*f.DeviceID))
	}
	if f.Sender != "" {
		where = append(where, "lower(sender) = lower("+arg(f.Sender)+")")
	}
	if f.ReceivedFrom != nil {
		where = append(where, "received_at >= "+arg(*f.ReceivedFrom))
	}
	if f.ReceivedTo != nil {
		where = append(where, "received_at < "+arg(*f.ReceivedTo))
	}
	if f.After != nil {
		where = append(where, "(received_at, id) < ("+arg(f.After.ReceivedAt)+", "+arg(f.After.ID)+")")
	}

	q := `SELECT ` + inboundColumns + ` FROM inbound_messages`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	q += ` ORDER BY received_at DESC, id DESC`
	if f.Limit > 0 {
		q += ` LIMIT ` + arg(f.Limit)
	}
	return s.query(ctx, q, args...)
}

func (s inboundStore) ClaimWebhooksDue(ctx context.Context, now time.Time, limit int) ([]*store.InboundMessage, error) {
	const q = `
		SELECT ` + inboundColumns + `
		FROM inbound_messages
		WHERE webhook_delivered_at IS NULL
		  AND webhook_url <> ''
		  AND webhook_next_at IS NOT NULL
		  AND webhook_next_at <= $1
		ORDER BY webhook_next_at
		LIMIT $2
		FOR UPDATE SKIP LOCKED`
	return s.query(ctx, q, now, limit)
}

func (s inboundStore) MarkWebhookDelivered(ctx context.Context, id uuid.UUID, at time.Time) error {
	return s.exec(ctx, `
		UPDATE inbound_messages SET webhook_delivered_at = $2, webhook_next_at = NULL
		WHERE id = $1`, id, at)
}

func (s inboundStore) ScheduleWebhookRetry(ctx context.Context, id uuid.UUID, nextAt time.Time, lastErr string) error {
	return s.exec(ctx, `
		UPDATE inbound_messages SET
			webhook_attempts = webhook_attempts + 1, webhook_next_at = $2, webhook_last_error = $3
		WHERE id = $1`, id, nextAt, lastErr)
}

func (s inboundStore) AbandonWebhook(ctx context.Context, id uuid.UUID, reason string) error {
	return s.exec(ctx, `
		UPDATE inbound_messages SET
			webhook_attempts = webhook_attempts + 1, webhook_next_at = NULL, webhook_last_error = $2
		WHERE id = $1`, id, reason)
}

func (s inboundStore) exec(ctx context.Context, q string, args ...any) error {
	tag, err := s.q.Exec(ctx, q, args...)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s inboundStore) query(ctx context.Context, q string, args ...any) ([]*store.InboundMessage, error) {
	rows, err := s.q.Query(ctx, q, args...)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	var out []*store.InboundMessage
	for rows.Next() {
		m, err := scanInbound(rows)
		if err != nil {
			return nil, translate(err)
		}
		out = append(out, m)
	}
	return out, translate(rows.Err())
}

func scanInbound(row scanner) (*store.InboundMessage, error) {
	var m store.InboundMessage
	err := row.Scan(
		&m.ID, &m.AccountID, &m.DeviceID, &m.DeviceRef, &m.SubscriptionID, &m.Sender, &m.Body,
		&m.ReceivedAt, &m.CreatedAt, &m.AutomationID, &m.Fields,
		&m.Webhook.URL, &m.Webhook.Attempts, &m.Webhook.NextAttemptAt, &m.Webhook.DeliveredAt, &m.Webhook.LastError,
	)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// fieldsOrEmpty stores no fields as {} rather than NULL.
func fieldsOrEmpty(f map[string]string) map[string]string {
	if f == nil {
		return map[string]string{}
	}
	return f
}
