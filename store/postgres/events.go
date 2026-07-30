package postgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/store"
)

type eventStore struct{ q querier }

// Append writes one entry to the audit trail. There is no update or delete
// here, deliberately: an event that could be rewritten would not be worth
// reading.
func (e eventStore) Append(ctx context.Context, in *store.JobEvent) error {
	const q = `
		INSERT INTO job_events (job_id, status, device_id, reason, created_at)
		VALUES ($1, $2, $3, $4, COALESCE($5, now()))`

	// status is nullable in the schema: an event that is not a state transition
	// (a retry, a callback attempt) records no status, and NULL says that where
	// an empty string would look like a status nobody set.
	var status *string
	if in.Status != "" {
		s := string(in.Status)
		status = &s
	}

	var createdAt any
	if !in.CreatedAt.IsZero() {
		createdAt = in.CreatedAt
	}

	_, err := e.q.Exec(ctx, q, in.JobID, status, in.DeviceID, in.Reason, createdAt)
	return translate(err)
}

func (e eventStore) ListByJob(ctx context.Context, jobID uuid.UUID) ([]*store.JobEvent, error) {
	const q = `
		SELECT id, job_id, status, device_id, reason, created_at
		FROM job_events
		WHERE job_id = $1
		ORDER BY created_at, id`

	rows, err := e.q.Query(ctx, q, jobID)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	var out []*store.JobEvent
	for rows.Next() {
		var (
			ev     store.JobEvent
			status *string
		)
		if err := rows.Scan(&ev.ID, &ev.JobID, &status, &ev.DeviceID, &ev.Reason, &ev.CreatedAt); err != nil {
			return nil, translate(err)
		}
		if status != nil {
			ev.Status = store.JobStatus(*status)
		}
		out = append(out, &ev)
	}
	return out, translate(rows.Err())
}
