package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/store"
)

type deviceStore struct{ q querier }

// deviceColumns is the projection every device read uses. Written once so a
// column added to the table cannot be picked up by one query and missed by
// another.
const deviceColumns = `
	id, label, phone_number, enabled,
	manufacturer, model, os_version, agent_version, carrier,
	battery_level, is_charging, signal_strength, network_type,
	sim_ready, sent_last_hour, permissions_ok, health_reported_at,
	last_seen_at, created_at, updated_at`

func (d deviceStore) Create(ctx context.Context, in *store.Device, tokenHash string) (*store.Device, error) {
	const q = `
		INSERT INTO devices (
			label, phone_number, token_hash, enabled,
			manufacturer, model, os_version, agent_version, carrier
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING ` + deviceColumns

	row := d.q.QueryRow(ctx, q,
		in.Label, in.PhoneNumber, tokenHash, true,
		in.Manufacturer, in.Model, in.OSVersion, in.AgentVersion, in.Carrier,
	)
	dev, err := scanDevice(row)
	return dev, translate(err)
}

func (d deviceStore) Get(ctx context.Context, id uuid.UUID) (*store.Device, error) {
	dev, err := scanDevice(d.q.QueryRow(ctx,
		`SELECT `+deviceColumns+` FROM devices WHERE id = $1`, id))
	return dev, translate(err)
}

// GetByTokenHash runs on every message of every stream, so it is a single
// indexed lookup on the unique index over token_hash.
func (d deviceStore) GetByTokenHash(ctx context.Context, tokenHash string) (*store.Device, error) {
	dev, err := scanDevice(d.q.QueryRow(ctx,
		`SELECT `+deviceColumns+` FROM devices WHERE token_hash = $1`, tokenHash))
	return dev, translate(err)
}

// List is unpaginated: the fleet is one row per physical phone.
func (d deviceStore) List(ctx context.Context) ([]*store.Device, error) {
	rows, err := d.q.Query(ctx,
		`SELECT `+deviceColumns+` FROM devices ORDER BY created_at DESC`)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	var out []*store.Device
	for rows.Next() {
		dev, err := scanDevice(rows)
		if err != nil {
			return nil, translate(err)
		}
		out = append(out, dev)
	}
	return out, translate(rows.Err())
}

func (d deviceStore) UpdateInfo(ctx context.Context, id uuid.UUID, in *store.Device) error {
	const q = `
		UPDATE devices SET
			label = $2, phone_number = $3, manufacturer = $4, model = $5,
			os_version = $6, agent_version = $7, carrier = $8, updated_at = now()
		WHERE id = $1`

	tag, err := d.q.Exec(ctx, q, id,
		in.Label, in.PhoneNumber, in.Manufacturer, in.Model,
		in.OSVersion, in.AgentVersion, in.Carrier,
	)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// Touch writes liveness and the latest health in one statement, because it runs
// on every heartbeat of every device.
//
// A nil health leaves the stored snapshot alone rather than blanking it: a
// heartbeat that carried no health is not evidence that the phone has none.
func (d deviceStore) Touch(ctx context.Context, id uuid.UUID, h *store.DeviceHealth, seenAt time.Time) error {
	const q = `
		UPDATE devices SET
			last_seen_at       = $2,
			battery_level      = COALESCE($3,  battery_level),
			is_charging        = COALESCE($4,  is_charging),
			signal_strength    = COALESCE($5,  signal_strength),
			network_type       = COALESCE($6,  network_type),
			sim_ready          = COALESCE($7,  sim_ready),
			sent_last_hour     = COALESCE($8,  sent_last_hour),
			permissions_ok     = COALESCE($9,  permissions_ok),
			health_reported_at = COALESCE($10, health_reported_at),
			updated_at         = $2
		WHERE id = $1`

	var (
		battery, signal, sent      *int
		charging, sim, permissions *bool
		network                    *string
		reportedAt                 *time.Time
	)
	if h != nil {
		battery, signal, sent = &h.BatteryLevel, &h.SignalStrength, &h.SentLastHour
		charging, sim, permissions = &h.IsCharging, &h.SimReady, &h.PermissionsOK
		network = &h.NetworkType
		at := h.ReportedAt
		if at.IsZero() {
			at = seenAt
		}
		reportedAt = &at
	}

	tag, err := d.q.Exec(ctx, q, id, seenAt,
		battery, charging, signal, network, sim, sent, permissions, reportedAt)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (d deviceStore) SetEnabled(ctx context.Context, id uuid.UUID, enabled bool) error {
	tag, err := d.q.Exec(ctx,
		`UPDATE devices SET enabled = $2, updated_at = now() WHERE id = $1`, id, enabled)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// scanner is satisfied by both pgx.Row and pgx.Rows, so one scan function
// serves single-row and multi-row queries.
type scanner interface {
	Scan(dest ...any) error
}

func scanDevice(row scanner) (*store.Device, error) {
	var (
		d          store.Device
		battery    *int
		charging   *bool
		signal     *int
		network    *string
		sim        *bool
		sentLast   *int
		perms      *bool
		reportedAt *time.Time
	)

	err := row.Scan(
		&d.ID, &d.Label, &d.PhoneNumber, &d.Enabled,
		&d.Manufacturer, &d.Model, &d.OSVersion, &d.AgentVersion, &d.Carrier,
		&battery, &charging, &signal, &network,
		&sim, &sentLast, &perms, &reportedAt,
		&d.LastSeenAt, &d.CreatedAt, &d.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	// health_reported_at is the discriminator: a device that has never reported
	// gets a nil Health rather than a zeroed struct, so "no report" cannot be
	// read as "battery empty, no signal".
	if reportedAt != nil {
		d.Health = &store.DeviceHealth{
			BatteryLevel:   deref(battery),
			IsCharging:     deref(charging),
			SignalStrength: deref(signal),
			NetworkType:    deref(network),
			SimReady:       deref(sim),
			SentLastHour:   deref(sentLast),
			PermissionsOK:  deref(perms),
			ReportedAt:     *reportedAt,
		}
	}
	return &d, nil
}

func deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}
