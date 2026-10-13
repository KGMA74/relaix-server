package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/KGMA74/relaix-server/store"
)

type runStore struct{ q querier }

const runColumns = `
	id, account_id, device_id, device_ref, automation_id, sender,
	started_at, finished_at, ok, error, steps, created_at`

func (s runStore) Record(ctx context.Context, in *store.AutomationRun) (*store.AutomationRun, bool, error) {
	steps := in.Steps
	if steps == nil {
		steps = []store.AutomationStep{}
	}
	stepsDoc, err := json.Marshal(steps)
	if err != nil {
		return nil, false, err
	}
	const q = `
		INSERT INTO automation_runs (
			account_id, device_id, device_ref, automation_id, sender,
			started_at, finished_at, ok, error, steps
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (device_id, device_ref) DO NOTHING
		RETURNING ` + runColumns
	r, err := scanRun(s.q.QueryRow(ctx, q,
		store.AccountOrDefault(in.AccountID), in.DeviceID, in.DeviceRef, in.AutomationID, in.Sender,
		in.StartedAt, in.FinishedAt, in.OK, in.Error, stepsDoc,
	))
	if err == nil {
		return r, true, nil
	}
	if err = translate(err); !errors.Is(err, store.ErrNotFound) {
		return nil, false, err
	}
	existing, err := scanRun(s.q.QueryRow(ctx,
		`SELECT `+runColumns+` FROM automation_runs WHERE device_id = $1 AND device_ref = $2`,
		in.DeviceID, in.DeviceRef))
	return existing, false, translate(err)
}

func (s runStore) List(ctx context.Context, f store.RunFilter) ([]*store.AutomationRun, error) {
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
	if f.AutomationID != "" {
		where = append(where, "automation_id = "+arg(f.AutomationID))
	}
	q := `SELECT ` + runColumns + ` FROM automation_runs`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	q += ` ORDER BY started_at DESC, id DESC`
	if f.Limit > 0 {
		q += ` LIMIT ` + arg(f.Limit)
	}
	rows, err := s.q.Query(ctx, q, args...)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	var out []*store.AutomationRun
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, translate(err)
		}
		out = append(out, r)
	}
	return out, translate(rows.Err())
}

func scanRun(row scanner) (*store.AutomationRun, error) {
	var (
		r     store.AutomationRun
		steps []byte
	)
	err := row.Scan(&r.ID, &r.AccountID, &r.DeviceID, &r.DeviceRef, &r.AutomationID, &r.Sender,
		&r.StartedAt, &r.FinishedAt, &r.OK, &r.Error, &steps, &r.CreatedAt)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(steps, &r.Steps); err != nil {
		return nil, err
	}
	return &r, nil
}
