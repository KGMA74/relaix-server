package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Errors returned across the persistence boundary. Callers match on these with
// errors.Is rather than inspecting driver errors, so that nothing above this
// package has to know what database is underneath.
var (
	// ErrNotFound is returned when a lookup by id or token finds nothing.
	ErrNotFound = errors.New("store: not found")

	// ErrConflict is returned when a write loses a race it was required to
	// win — a token already consumed, a job already moved on, a duplicate
	// token hash. It always means "retry or report", never "retry blindly".
	ErrConflict = errors.New("store: conflict")

	// ErrTokenExpired distinguishes an enrollment token that ran out of time
	// from one that was already used, because the operator's fix differs: mint
	// a new token, versus find out which phone claimed the last one.
	ErrTokenExpired = errors.New("store: enrollment token expired")
)

// Store is the aggregate entry point. Components take the narrow interface they
// need — a scheduler wants Jobs and Devices, the enrollment service wants
// Enrollments — but transactions need the whole set, so this composes them.
type Store interface {
	Devices() DeviceStore
	Jobs() JobStore
	Enrollments() EnrollmentStore
	Events() EventStore

	// WithTx runs fn inside a transaction, passing a Store scoped to it.
	// Returning a non-nil error rolls back. Nesting is not supported: the Store
	// handed to fn must not be used to open another transaction.
	//
	// This exists because the operations that matter here are composite. The
	// scheduler must claim a job and mark it assigned as one step or two
	// instances will dispatch the same SMS twice; enrollment must consume a
	// token and create a device together or a crash leaves a burnt token and no
	// phone.
	WithTx(ctx context.Context, fn func(Store) error) error
}

// DeviceStore persists enrolled phones.
type DeviceStore interface {
	// Create records a newly enrolled device. tokenHash is the hash of the
	// long-lived device token; the plaintext is returned to the agent once and
	// never stored.
	Create(ctx context.Context, d *Device, tokenHash string) (*Device, error)

	// Get returns a device by id, or ErrNotFound.
	Get(ctx context.Context, id uuid.UUID) (*Device, error)

	// GetByTokenHash authenticates a device. This runs on every message of
	// every stream — see the per-message token decision in docs/protocol.md —
	// so it is the one lookup that must stay cheap.
	GetByTokenHash(ctx context.Context, tokenHash string) (*Device, error)

	// List returns all devices, newest first. The fleet is small by nature
	// (one row per physical phone), so this is deliberately unpaginated.
	List(ctx context.Context) ([]*Device, error)

	// UpdateInfo refreshes the descriptive fields from a Register, so the
	// server's view tracks OS updates, app updates and SIM swaps.
	UpdateInfo(ctx context.Context, id uuid.UUID, d *Device) error

	// Touch records liveness and the latest health snapshot. Called on every
	// heartbeat, so implementations should keep it to a single statement.
	Touch(ctx context.Context, id uuid.UUID, h *DeviceHealth, seenAt time.Time) error

	// SetEnabled flips the operator kill switch.
	SetEnabled(ctx context.Context, id uuid.UUID, enabled bool) error

	// Delete removes a device permanently, or returns ErrNotFound.
	//
	// Jobs survive: their device columns are ON DELETE SET NULL, so the
	// history of what was sent stays readable after the phone that sent it is
	// gone. Retiring a handset must not erase the record of its work.
	//
	// This is the harsher of the two operator actions. SetEnabled(false) is
	// reversible and keeps the row visible; Delete is for a phone that will
	// never come back — sold, lost, or an enrollment that was replaced.
	Delete(ctx context.Context, id uuid.UUID) error
}

// JobStore persists SMS requests and their outcomes.
type JobStore interface {
	// Create stores a new job in JobPending.
	Create(ctx context.Context, j *Job) (*Job, error)

	// Get returns a job by id, or ErrNotFound.
	Get(ctx context.Context, id uuid.UUID) (*Job, error)

	// ClaimSchedulable returns up to limit pending jobs that are eligible now —
	// not held back by ScheduledAt, not expired — in priority order, and locks
	// them for the duration of the caller's transaction so that a second
	// scheduler skips them rather than blocking.
	//
	// Must be called inside WithTx. The lock is what keeps V2's multiple
	// instances from dispatching one SMS twice; taking it here rather than in
	// the scheduler keeps that guarantee in one place.
	ClaimSchedulable(ctx context.Context, now time.Time, limit int) ([]*Job, error)

	// MarkAssigned records that a job was pushed to a device and accepted,
	// incrementing Attempts. Returns ErrConflict if the job is no longer
	// pending — it was cancelled, or another instance got there first.
	MarkAssigned(ctx context.Context, id, deviceID uuid.UUID, at time.Time) error

	// Release returns an assigned job to pending, for when a device refused it
	// or disconnected still holding it. Attempts is not decremented: the point
	// of the counter is to bound how often we try, not how often we succeed.
	Release(ctx context.Context, id uuid.UUID, reason string) error

	// Complete records a terminal outcome from a JobResult. Results are
	// at-least-once, so a repeat for a job already in the same terminal state
	// must be a no-op rather than an error; a late DELIVERED after SENT is a
	// legitimate transition and must be accepted.
	Complete(ctx context.Context, id uuid.UUID, r JobResult) error

	// Cancel withdraws a job. Returns ErrConflict once the job has reached a
	// terminal state, because there is no recalling a message the handset has
	// already passed to the network.
	Cancel(ctx context.Context, id uuid.UUID, reason string) error

	// ListAssignedTo returns the jobs a device is currently believed to hold,
	// so a reconnect can be reconciled against what the agent reports instead
	// of blindly redispatching.
	ListAssignedTo(ctx context.Context, deviceID uuid.UUID) ([]*Job, error)

	// ClaimCallbacksDue returns up to limit jobs whose webhook is owed and due,
	// locked for the caller's transaction. Same contract as ClaimSchedulable.
	ClaimCallbacksDue(ctx context.Context, now time.Time, limit int) ([]*Job, error)

	// MarkCallbackDelivered records a webhook the receiver accepted.
	MarkCallbackDelivered(ctx context.Context, id uuid.UUID, at time.Time) error

	// ScheduleCallbackRetry records a failed attempt and when to try next. The
	// backoff schedule is the watcher's decision, not the store's — this only
	// persists it.
	ScheduleCallbackRetry(ctx context.Context, id uuid.UUID, nextAt time.Time, lastErr string) error

	// AbandonCallback stops retrying without claiming success: it records the
	// final failure and clears the schedule, so the job is never claimed again.
	//
	// Separate from MarkCallbackDelivered because giving up is not delivery.
	// Reusing the delivered flag would make "which callbacks reached the
	// caller" unanswerable, which is exactly the question an operator asks
	// after an outage.
	AbandonCallback(ctx context.Context, id uuid.UUID, reason string) error
}

// JobResult is the outcome of a send, as reported by a device.
type JobResult struct {
	Status       JobStatus
	ErrorCode    string
	ErrorMessage string
	PartsSent    int
	CompletedAt  time.Time
}

// EnrollmentStore persists the single-use tokens that let a phone join.
type EnrollmentStore interface {
	// Create mints a token. tokenHash is the hash of the value encoded in the
	// QR code; the plaintext is shown to the operator once and never stored.
	Create(ctx context.Context, tokenHash string, expiresAt time.Time) (*EnrollmentToken, error)

	// Consume redeems a token for the given device, atomically. This is the
	// single point that makes a photographed QR code worthless after first use,
	// so it must be one conditional write, not a read followed by a write:
	//
	//   ErrNotFound     no such token
	//   ErrTokenExpired the token ran out of time
	//   ErrConflict     the token was already redeemed
	Consume(ctx context.Context, tokenHash string, deviceID uuid.UUID, at time.Time) (*EnrollmentToken, error)

	// DeleteExpired sweeps tokens that ran out without being redeemed, and
	// reports how many it removed.
	DeleteExpired(ctx context.Context, before time.Time) (int64, error)
}

// EventStore is the append-only audit trail. It has no update or delete: an
// event that could be rewritten would not be worth reading.
type EventStore interface {
	// Append records one entry. A failure here must never fail the operation
	// being audited — losing a log line is bad, losing an SMS is worse — so
	// callers log and continue rather than propagating.
	Append(ctx context.Context, e *JobEvent) error

	// ListByJob returns one job's history in chronological order.
	ListByJob(ctx context.Context, jobID uuid.UUID) ([]*JobEvent, error)
}
