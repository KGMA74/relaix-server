package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/policy"
	"github.com/KGMA74/relaix-server/store"
)

type deviceStore struct{ q querier }

// deviceColumns is the projection every device read uses. Written once so a
// column added to the table cannot be picked up by one query and missed by
// another.
const deviceColumns = `
	id, account_id, label, phone_number, enabled,
	manufacturer, model, os_version, agent_version, carrier, sim_info, hardware_hash,
	time_zone, policy, capabilities,
	battery_level, is_charging, signal_strength, network_type,
	sim_ready, sent_last_hour, permissions_ok, sim_health, health_reported_at,
	last_seen_at, created_at, updated_at`

// simInfoRow and simHealthRow are the JSON shape of the sim_info / sim_health
// columns. Kept separate from store.SimInfo/SimHealth rather than tagging
// those directly: nothing else in store carries json tags, and the wire
// representation of a column is this package's concern, not the domain
// type's.
type simInfoRow struct {
	SubscriptionID int32  `json:"subscriptionId"`
	SlotIndex      int32  `json:"slotIndex"`
	PhoneNumber    string `json:"phoneNumber,omitempty"`
	Carrier        string `json:"carrier,omitempty"`
	DisplayName    string `json:"displayName,omitempty"`
	IsDefaultSMS   bool   `json:"isDefaultSms,omitempty"`
}

type simHealthRow struct {
	SubscriptionID int32 `json:"subscriptionId"`
	SignalStrength int   `json:"signalStrength"`
	SimReady       bool  `json:"simReady"`
	SentLastHour   int   `json:"sentLastHour"`
}

func marshalSimInfo(sims []store.SimInfo) ([]byte, error) {
	rows := make([]simInfoRow, len(sims))
	for i, s := range sims {
		rows[i] = simInfoRow{
			SubscriptionID: s.SubscriptionID, SlotIndex: s.SlotIndex,
			PhoneNumber: s.PhoneNumber, Carrier: s.Carrier,
			DisplayName: s.DisplayName, IsDefaultSMS: s.IsDefaultSMS,
		}
	}
	return json.Marshal(rows)
}

func unmarshalSimInfo(raw []byte) ([]store.SimInfo, error) {
	var rows []simInfoRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	out := make([]store.SimInfo, len(rows))
	for i, r := range rows {
		out[i] = store.SimInfo{
			SubscriptionID: r.SubscriptionID, SlotIndex: r.SlotIndex,
			PhoneNumber: r.PhoneNumber, Carrier: r.Carrier,
			DisplayName: r.DisplayName, IsDefaultSMS: r.IsDefaultSMS,
		}
	}
	return out, nil
}

func marshalSimHealth(sims []store.SimHealth) ([]byte, error) {
	rows := make([]simHealthRow, len(sims))
	for i, s := range sims {
		rows[i] = simHealthRow{
			SubscriptionID: s.SubscriptionID, SignalStrength: s.SignalStrength,
			SimReady: s.SimReady, SentLastHour: s.SentLastHour,
		}
	}
	return json.Marshal(rows)
}

func unmarshalSimHealth(raw []byte) ([]store.SimHealth, error) {
	var rows []simHealthRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	out := make([]store.SimHealth, len(rows))
	for i, r := range rows {
		out[i] = store.SimHealth{
			SubscriptionID: r.SubscriptionID, SignalStrength: r.SignalStrength,
			SimReady: r.SimReady, SentLastHour: r.SentLastHour,
		}
	}
	return out, nil
}

func (d deviceStore) Create(ctx context.Context, in *store.Device, tokenHash string) (*store.Device, error) {
	const q = `
		INSERT INTO devices (
			label, phone_number, token_hash, enabled,
			manufacturer, model, os_version, agent_version, carrier, sim_info,
			hardware_hash, time_zone, account_id, capabilities
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULLIF($11, ''),$12,$13,$14)
		RETURNING ` + deviceColumns

	simInfo, err := marshalSimInfo(in.Sims)
	if err != nil {
		return nil, err
	}
	row := d.q.QueryRow(ctx, q,
		in.Label, in.PhoneNumber, tokenHash, true,
		in.Manufacturer, in.Model, in.OSVersion, in.AgentVersion, in.Carrier, simInfo,
		in.HardwareHash, in.TimeZone, store.AccountOrDefault(in.AccountID), nonNil(in.Capabilities),
	)
	dev, err := scanDevice(row)
	return dev, translate(err)
}

// GetByHardwareHash runs once per enrollment, on the partial unique index.
func (d deviceStore) GetByHardwareHash(ctx context.Context, hardwareHash string) (*store.Device, error) {
	if hardwareHash == "" {
		return nil, store.ErrNotFound
	}
	dev, err := scanDevice(d.q.QueryRow(ctx,
		`SELECT `+deviceColumns+` FROM devices WHERE hardware_hash = $1`, hardwareHash))
	return dev, translate(err)
}

// Rekey replaces the token in the same statement that refreshes the
// description, so there is no moment where the row carries the new
// description under the old credential.
func (d deviceStore) Rekey(ctx context.Context, id uuid.UUID, in *store.Device, tokenHash string) (*store.Device, error) {
	const q = `
		UPDATE devices SET
			token_hash = $2, enabled = true,
			label = $3, phone_number = $4, manufacturer = $5, model = $6,
			os_version = $7, agent_version = $8, carrier = $9, sim_info = $10,
			time_zone = $11, account_id = $12, capabilities = $13, updated_at = now()
		WHERE id = $1
		RETURNING ` + deviceColumns

	simInfo, err := marshalSimInfo(in.Sims)
	if err != nil {
		return nil, err
	}
	// The account moves with the enrollment: a phone re-enrolled with
	// another account's code now belongs to that account.
	dev, err := scanDevice(d.q.QueryRow(ctx, q, id, tokenHash,
		in.Label, in.PhoneNumber, in.Manufacturer, in.Model,
		in.OSVersion, in.AgentVersion, in.Carrier, simInfo, in.TimeZone,
		store.AccountOrDefault(in.AccountID), nonNil(in.Capabilities),
	))
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
func (d deviceStore) List(ctx context.Context, scope store.Scope) ([]*store.Device, error) {
	rows, err := d.q.Query(ctx,
		`SELECT `+deviceColumns+` FROM devices
		WHERE $1::uuid IS NULL OR account_id = $1
		ORDER BY created_at DESC`, scope.AccountID)
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
			os_version = $6, agent_version = $7, carrier = $8, sim_info = $9,
			time_zone = $10, capabilities = $11, updated_at = now()
		WHERE id = $1`

	simInfo, err := marshalSimInfo(in.Sims)
	if err != nil {
		return err
	}
	tag, err := d.q.Exec(ctx, q, id,
		in.Label, in.PhoneNumber, in.Manufacturer, in.Model,
		in.OSVersion, in.AgentVersion, in.Carrier, simInfo, in.TimeZone,
		nonNil(in.Capabilities),
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
			sim_health         = COALESCE($10, sim_health),
			health_reported_at = COALESCE($11, health_reported_at),
			updated_at         = $2
		WHERE id = $1`

	var (
		battery, signal, sent      *int
		charging, sim, permissions *bool
		network                    *string
		simHealth                  []byte
		reportedAt                 *time.Time
	)
	if h != nil {
		battery, signal, sent = &h.BatteryLevel, &h.SignalStrength, &h.SentLastHour
		charging, sim, permissions = &h.IsCharging, &h.SimReady, &h.PermissionsOK
		network = &h.NetworkType
		var err error
		simHealth, err = marshalSimHealth(h.Sims)
		if err != nil {
			return err
		}
		at := h.ReportedAt
		if at.IsZero() {
			at = seenAt
		}
		reportedAt = &at
	}

	tag, err := d.q.Exec(ctx, q, id, seenAt,
		battery, charging, signal, network, sim, sent, permissions, simHealth, reportedAt)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// SetPolicy bumps the version inside the same statement that writes the
// document, so two concurrent edits cannot both claim the same version.
func (d deviceStore) SetPolicy(ctx context.Context, id uuid.UUID, p policy.Policy) (policy.Policy, error) {
	const q = `
		UPDATE devices SET
			policy = $2::jsonb || jsonb_build_object(
				'version', COALESCE((policy->>'version')::bigint, 0) + 1,
				'updatedAt', to_jsonb(now())),
			updated_at = now()
		WHERE id = $1
		RETURNING policy`

	p.Version, p.UpdatedAt = 0, nil
	doc, err := json.Marshal(p)
	if err != nil {
		return policy.Policy{}, err
	}
	var raw []byte
	if err := d.q.QueryRow(ctx, q, id, doc).Scan(&raw); err != nil {
		return policy.Policy{}, translate(err)
	}
	var stored policy.Policy
	if err := json.Unmarshal(raw, &stored); err != nil {
		return policy.Policy{}, fmt.Errorf("postgres: decode policy: %w", err)
	}
	return stored, nil
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

func (d deviceStore) Delete(ctx context.Context, id uuid.UUID) error {
	// No cascade needed here: jobs.requested_device_id and
	// jobs.assigned_device_id are ON DELETE SET NULL, so the record of what
	// this phone sent outlives the phone.
	tag, err := d.q.Exec(ctx, `DELETE FROM devices WHERE id = $1`, id)
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
		simInfo    []byte
		battery    *int
		charging   *bool
		signal     *int
		network    *string
		sim        *bool
		sentLast   *int
		perms      *bool
		simHealth  []byte
		reportedAt *time.Time
		hardware   *string
		policyDoc  []byte
	)

	err := row.Scan(
		&d.ID, &d.AccountID, &d.Label, &d.PhoneNumber, &d.Enabled,
		&d.Manufacturer, &d.Model, &d.OSVersion, &d.AgentVersion, &d.Carrier, &simInfo, &hardware,
		&d.TimeZone, &policyDoc, &d.Capabilities,
		&battery, &charging, &signal, &network,
		&sim, &sentLast, &perms, &simHealth, &reportedAt,
		&d.LastSeenAt, &d.CreatedAt, &d.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	d.HardwareHash = deref(hardware)
	if len(policyDoc) > 0 {
		if err := json.Unmarshal(policyDoc, &d.Policy); err != nil {
			return nil, fmt.Errorf("postgres: decode policy: %w", err)
		}
	}

	d.Sims, err = unmarshalSimInfo(simInfo)
	if err != nil {
		return nil, err
	}

	// health_reported_at is the discriminator: a device that has never reported
	// gets a nil Health rather than a zeroed struct, so "no report" cannot be
	// read as "battery empty, no signal".
	if reportedAt != nil {
		sims, err := unmarshalSimHealth(simHealth)
		if err != nil {
			return nil, err
		}
		d.Health = &store.DeviceHealth{
			BatteryLevel:   deref(battery),
			IsCharging:     deref(charging),
			SignalStrength: deref(signal),
			NetworkType:    deref(network),
			SimReady:       deref(sim),
			SentLastHour:   deref(sentLast),
			PermissionsOK:  deref(perms),
			ReportedAt:     *reportedAt,
			Sims:           sims,
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
