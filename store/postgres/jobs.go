package postgres

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/store"
)

type jobStore struct{ q querier }

const jobColumns = `
	id, recipient, body, mode, priority,
	scheduled_at, expires_at, requested_device_id, assigned_device_id, subscription_id,
	assigned_subscription_id, account_id,
	status, error_code, error_message, parts_sent, attempts,
	callback_url, callback_attempts, callback_next_at, callback_delivered_at, callback_last_error,
	created_at, updated_at, assigned_at, completed_at`

func (j jobStore) Create(ctx context.Context, in *store.Job) (*store.Job, error) {
	const q = `
		INSERT INTO jobs (
			recipient, body, mode, priority, scheduled_at, expires_at,
			requested_device_id, subscription_id, callback_url, callback_next_at,
			account_id
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		RETURNING ` + jobColumns

	row := j.q.QueryRow(ctx, q,
		in.Recipient, in.Body, string(in.Mode), in.Priority,
		in.ScheduledAt, in.ExpiresAt, in.RequestedDeviceID, in.SubscriptionID,
		in.Callback.URL, in.Callback.NextAttemptAt,
		store.AccountOrDefault(in.AccountID),
	)
	job, err := scanJob(row)
	return job, translate(err)
}

func (j jobStore) Get(ctx context.Context, id uuid.UUID) (*store.Job, error) {
	job, err := scanJob(j.q.QueryRow(ctx,
		`SELECT `+jobColumns+` FROM jobs WHERE id = $1`, id))
	return job, translate(err)
}

// ClaimSchedulable takes a row lock on everything it returns.
//
// FOR UPDATE SKIP LOCKED rather than plain FOR UPDATE: a second scheduler must
// step over rows the first is already placing and get on with other work, not
// queue behind them. That is what keeps two instances from dispatching one SMS
// twice without either of them waiting on the other.
//
// Expired jobs are returned rather than filtered out. They cannot be sent, but
// somebody has to mark them failed and record why, and the scheduler is the
// component that does it.
func (j jobStore) ClaimSchedulable(ctx context.Context, now time.Time, limit int) ([]*store.Job, error) {
	const q = `
		SELECT ` + jobColumns + `
		FROM jobs
		WHERE status = 'pending'
		  AND (scheduled_at IS NULL OR scheduled_at <= $1)
		ORDER BY priority DESC, scheduled_at NULLS FIRST, created_at
		LIMIT $2
		FOR UPDATE SKIP LOCKED`

	return j.queryJobs(ctx, q, now, limit)
}

func (j jobStore) MarkAssigned(ctx context.Context, id, deviceID uuid.UUID, subscriptionID int32, at time.Time) error {
	const q = `
		UPDATE jobs SET
			status = 'assigned', assigned_device_id = $2, assigned_at = $3,
			assigned_subscription_id = NULLIF($4, 0),
			attempts = attempts + 1, updated_at = $3
		WHERE id = $1 AND status = 'pending'`

	tag, err := j.q.Exec(ctx, q, id, deviceID, at, subscriptionID)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		// Either the job is gone or it is no longer pending. Both mean the same
		// thing to the caller: it lost the race, skip and move on.
		return j.missingOrConflict(ctx, id)
	}
	return nil
}

// CountDispatched walks jobs_assigned_recent_idx over the last day only.
func (j jobStore) CountDispatched(ctx context.Context, hourAgo, dayAgo time.Time) ([]store.DispatchCount, error) {
	const q = `
		SELECT assigned_device_id, assigned_subscription_id,
			count(*) FILTER (WHERE assigned_at >= $1),
			count(*)
		FROM jobs
		WHERE assigned_at >= $2
			AND assigned_device_id IS NOT NULL
			AND status IN ('assigned', 'sent', 'delivered')
		GROUP BY assigned_device_id, assigned_subscription_id`

	rows, err := j.q.Query(ctx, q, hourAgo, dayAgo)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	var out []store.DispatchCount
	for rows.Next() {
		var c store.DispatchCount
		if err := rows.Scan(&c.DeviceID, &c.SubscriptionID, &c.LastHour, &c.LastDay); err != nil {
			return nil, translate(err)
		}
		out = append(out, c)
	}
	return out, translate(rows.Err())
}

// Release returns an assigned job to pending. attempts is deliberately not
// decremented: the counter bounds how often we try, not how often we succeed.
func (j jobStore) Release(ctx context.Context, id uuid.UUID, reason string) error {
	const q = `
		UPDATE jobs SET
			status = 'pending', assigned_device_id = NULL, assigned_at = NULL,
			assigned_subscription_id = NULL,
			error_message = $2, updated_at = now()
		WHERE id = $1 AND status = 'assigned'`

	tag, err := j.q.Exec(ctx, q, id, reason)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return j.missingOrConflict(ctx, id)
	}
	return nil
}

// Complete records a terminal outcome.
//
// The WHERE clause encodes the at-least-once contract in SQL rather than in Go:
// a repeat of the status the job already holds is a no-op, a late DELIVERED
// after SENT is allowed because that is a real transition, and anything else
// arriving after a terminal state is refused.
func (j jobStore) Complete(ctx context.Context, id uuid.UUID, r store.JobResult) error {
	// parts_sent is only overwritten when the result carries a count. A late
	// DELIVERED report carries none, and a plain assignment would wipe the
	// number the SENT result gave — which is what callers reconcile cost
	// against.
	const q = `
		UPDATE jobs SET
			status = $2, error_code = $3, error_message = $4,
			parts_sent = CASE WHEN $5::int > 0 THEN $5::int ELSE parts_sent END,
			completed_at = $6, updated_at = $6
		WHERE id = $1
		  AND (
		    status NOT IN ('sent','delivered','failed','cancelled')
		    OR (status = 'sent' AND $2 = 'delivered')
		  )`

	tag, err := j.q.Exec(ctx, q, id,
		string(r.Status), r.ErrorCode, r.ErrorMessage, r.PartsSent, r.CompletedAt)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}

	// No row changed. A duplicate of the status already held is expected under
	// at-least-once delivery and must not surface as an error.
	current, err := j.Get(ctx, id)
	if err != nil {
		return err
	}
	if current.Status == r.Status {
		return nil
	}
	return store.ErrConflict
}

// Cancel refuses once the job is terminal: there is no recalling a message the
// handset has already passed to the network.
func (j jobStore) Cancel(ctx context.Context, id uuid.UUID, reason string) error {
	const q = `
		UPDATE jobs SET
			status = 'cancelled', error_message = $2,
			completed_at = now(), updated_at = now()
		WHERE id = $1 AND status NOT IN ('sent','delivered','failed','cancelled')`

	tag, err := j.q.Exec(ctx, q, id, reason)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return j.missingOrConflict(ctx, id)
	}
	return nil
}

func (j jobStore) ListAssignedTo(ctx context.Context, deviceID uuid.UUID) ([]*store.Job, error) {
	const q = `
		SELECT ` + jobColumns + `
		FROM jobs
		WHERE status = 'assigned' AND assigned_device_id = $1
		ORDER BY created_at`

	return j.queryJobs(ctx, q, deviceID)
}

// List builds its WHERE clause from whichever filters are set, always as
// positional arguments: the filter values come straight from a query string.
//
// The cursor is a row comparison, (created_at, id) < ($a, $b), which matches
// the ORDER BY term for term and so is served by jobs_created_idx as a single
// index range rather than a filter over everything newer.
func (j jobStore) List(ctx context.Context, f store.JobFilter) ([]*store.Job, error) {
	var (
		where []string
		args  []any
	)
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}

	// First, so the planner can lead with jobs_account_created_idx.
	if f.AccountID != nil {
		where = append(where, "account_id = "+arg(*f.AccountID))
	}
	if len(f.Statuses) > 0 {
		statuses := make([]string, len(f.Statuses))
		for i, s := range f.Statuses {
			statuses[i] = string(s)
		}
		where = append(where, "status = ANY("+arg(statuses)+")")
	}
	if f.DeviceID != nil {
		p := arg(*f.DeviceID)
		where = append(where, "(assigned_device_id = "+p+" OR requested_device_id = "+p+")")
	}
	if f.RecipientPrefix != "" {
		// starts_with rather than LIKE, so a stray % or _ in the input is a
		// character to match and not a wildcard.
		where = append(where, "starts_with(recipient, "+arg(f.RecipientPrefix)+")")
	}
	if f.CreatedFrom != nil {
		where = append(where, "created_at >= "+arg(*f.CreatedFrom))
	}
	if f.CreatedTo != nil {
		where = append(where, "created_at < "+arg(*f.CreatedTo))
	}
	if f.After != nil {
		where = append(where, "(created_at, id) < ("+arg(f.After.CreatedAt)+", "+arg(f.After.ID)+")")
	}

	q := `SELECT ` + jobColumns + ` FROM jobs`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	q += ` ORDER BY created_at DESC, id DESC`
	if f.Limit > 0 {
		q += ` LIMIT ` + arg(f.Limit)
	}

	return j.queryJobs(ctx, q, args...)
}

// CountByBucket walks jobs_created_idx over the window; date_bin aligns the
// buckets on from, so the caller's bucket boundaries are the database's.
func (j jobStore) CountByBucket(ctx context.Context, scope store.Scope, from time.Time, bucket time.Duration) ([]store.BucketCount, error) {
	const q = `
		SELECT date_bin(make_interval(secs => $2), created_at, $1) AS b, status, count(*)
		FROM jobs
		WHERE created_at >= $1 AND ($3::uuid IS NULL OR account_id = $3)
		GROUP BY b, status
		ORDER BY b`

	rows, err := j.q.Query(ctx, q, from, bucket.Seconds(), scope.AccountID)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	var out []store.BucketCount
	for rows.Next() {
		var (
			c      store.BucketCount
			status string
		)
		if err := rows.Scan(&c.Start, &status, &c.Count); err != nil {
			return nil, translate(err)
		}
		c.Status = store.JobStatus(status)
		out = append(out, c)
	}
	return out, translate(rows.Err())
}

func (j jobStore) CountByStatus(ctx context.Context, scope store.Scope, since time.Time) (map[store.JobStatus]int, error) {
	rows, err := j.q.Query(ctx,
		`SELECT status, count(*) FROM jobs
		WHERE created_at >= $1 AND ($2::uuid IS NULL OR account_id = $2)
		GROUP BY status`, since, scope.AccountID)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	out := make(map[store.JobStatus]int)
	for rows.Next() {
		var (
			status string
			n      int
		)
		if err := rows.Scan(&status, &n); err != nil {
			return nil, translate(err)
		}
		out[store.JobStatus(status)] = n
	}
	return out, translate(rows.Err())
}

// ClaimCallbacksDue locks what it returns, for the same reason as
// ClaimSchedulable: two watchers must not both POST the same webhook.
func (j jobStore) ClaimCallbacksDue(ctx context.Context, now time.Time, limit int) ([]*store.Job, error) {
	const q = `
		SELECT ` + jobColumns + `
		FROM jobs
		WHERE callback_delivered_at IS NULL
		  AND callback_url <> ''
		  AND callback_next_at IS NOT NULL
		  AND callback_next_at <= $1
		ORDER BY callback_next_at
		LIMIT $2
		FOR UPDATE SKIP LOCKED`

	return j.queryJobs(ctx, q, now, limit)
}

func (j jobStore) MarkCallbackDelivered(ctx context.Context, id uuid.UUID, at time.Time) error {
	const q = `
		UPDATE jobs SET
			callback_delivered_at = $2, callback_next_at = NULL, updated_at = $2
		WHERE id = $1`

	tag, err := j.q.Exec(ctx, q, id, at)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// AbandonCallback clears the schedule without touching callback_delivered_at,
// so the job is never claimed again and still reads as undelivered — because it
// is.
func (j jobStore) AbandonCallback(ctx context.Context, id uuid.UUID, reason string) error {
	const q = `
		UPDATE jobs SET
			callback_attempts = callback_attempts + 1,
			callback_next_at = NULL, callback_last_error = $2, updated_at = now()
		WHERE id = $1`

	tag, err := j.q.Exec(ctx, q, id, reason)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (j jobStore) ScheduleCallbackRetry(ctx context.Context, id uuid.UUID, nextAt time.Time, lastErr string) error {
	const q = `
		UPDATE jobs SET
			callback_attempts = callback_attempts + 1,
			callback_next_at = $2, callback_last_error = $3, updated_at = $2
		WHERE id = $1`

	tag, err := j.q.Exec(ctx, q, id, nextAt, lastErr)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// missingOrConflict distinguishes "no such job" from "the job moved on", after
// a conditional UPDATE matched nothing.
func (j jobStore) missingOrConflict(ctx context.Context, id uuid.UUID) error {
	var exists bool
	err := j.q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM jobs WHERE id = $1)`, id).Scan(&exists)
	if err != nil {
		return translate(err)
	}
	if !exists {
		return store.ErrNotFound
	}
	return store.ErrConflict
}

func (j jobStore) queryJobs(ctx context.Context, q string, args ...any) ([]*store.Job, error) {
	rows, err := j.q.Query(ctx, q, args...)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	var out []*store.Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, translate(err)
		}
		out = append(out, job)
	}
	return out, translate(rows.Err())
}

func scanJob(row scanner) (*store.Job, error) {
	var (
		j      store.Job
		mode   string
		status string
	)

	err := row.Scan(
		&j.ID, &j.Recipient, &j.Body, &mode, &j.Priority,
		&j.ScheduledAt, &j.ExpiresAt, &j.RequestedDeviceID, &j.AssignedDeviceID, &j.SubscriptionID,
		&j.AssignedSubscriptionID, &j.AccountID,
		&status, &j.ErrorCode, &j.ErrorMessage, &j.PartsSent, &j.Attempts,
		&j.Callback.URL, &j.Callback.Attempts, &j.Callback.NextAttemptAt,
		&j.Callback.DeliveredAt, &j.Callback.LastError,
		&j.CreatedAt, &j.UpdatedAt, &j.AssignedAt, &j.CompletedAt,
	)
	if err != nil {
		return nil, err
	}

	j.Mode = store.JobMode(mode)
	j.Status = store.JobStatus(status)
	return &j, nil
}
