// Package store defines the persistence boundary of the control plane.
//
// It holds the domain types and the interfaces over them; the Postgres
// implementation lives in a subpackage. Everything above this boundary — hub,
// scheduler, gRPC handlers, HTTP API — depends on these interfaces and never on
// a database driver, so each can be exercised against a fake.
//
// The types mirror the schema in db/migrations/00001_init.sql, which in turn
// mirrors the wire contract in the monorepo's proto/. Where the three disagree,
// the proto wins: it is the contract with devices we do not control.
package store

import (
	"time"

	"github.com/google/uuid"
)

// JobStatus is the lifecycle state of a job. The values match the CHECK
// constraint on jobs.status; they are a Go string type rather than an int so
// that a value read from the database is self-describing in a log line.
type JobStatus string

const (
	// JobPending is waiting for a device. The scheduler considers these.
	JobPending JobStatus = "pending"
	// JobAssigned has been pushed to a device that acknowledged it.
	JobAssigned JobStatus = "assigned"
	// JobSent means the handset handed the message to the network.
	JobSent JobStatus = "sent"
	// JobDelivered means the recipient's network returned a delivery report.
	// Many carriers never send one, so this is a bonus state, not the expected
	// terminal one — nothing may block waiting for it.
	JobDelivered JobStatus = "delivered"
	// JobFailed is terminal and carries an error code.
	JobFailed JobStatus = "failed"
	// JobCancelled was withdrawn before the handset passed it to the network.
	JobCancelled JobStatus = "cancelled"
)

// Terminal reports whether the job will not change state again on its own.
// Delivered is terminal even though Sent may still be followed by it: callers
// use this to decide when to fire a callback, and a second callback after a
// late delivery report is expected and safe.
func (s JobStatus) Terminal() bool {
	switch s {
	case JobSent, JobDelivered, JobFailed, JobCancelled:
		return true
	default:
		return false
	}
}

// JobMode decides what happens when no device can take a job right now.
type JobMode string

const (
	// ModeImmediate fails fast: if nothing can take the job, the caller is told
	// at once. For interactive traffic, where a late message is worse than a
	// clean error the caller can fall back from.
	ModeImmediate JobMode = "immediate"
	// ModeQueued persists the job and retries it on later ticks.
	ModeQueued JobMode = "queued"
)

// DeviceHealth is the last snapshot a device reported. It mirrors the proto
// message of the same name; see the monorepo's docs/protocol.md for why signal
// is a normalized level rather than a raw measurement.
type DeviceHealth struct {
	// BatteryLevel is 0-100.
	BatteryLevel int
	IsCharging   bool
	// SignalStrength is a normalized 0-4 level, not dBm. Android reports a
	// different raw scale per radio technology, so raw values are not
	// comparable across a mixed fleet. 0 means the device cannot send.
	SignalStrength int
	NetworkType    string
	SimReady       bool
	// SentLastHour is the agent's own count, used for per-device rate limiting.
	SentLastHour int
	// PermissionsOK reports whether SEND_SMS and friends are still granted. A
	// user can revoke them at any time; without this the failure mode is a
	// device that accepts jobs and fails every one.
	PermissionsOK bool
	ReportedAt    time.Time
}

// Device is an enrolled phone.
type Device struct {
	ID          uuid.UUID
	Label       string
	PhoneNumber string
	// Enabled is the operator kill switch. A disabled device is refused at
	// Register and never scheduled, without losing its history.
	Enabled bool

	Manufacturer string
	Model        string
	OSVersion    string
	AgentVersion string
	Carrier      string

	// Health is nil for a device that enrolled but has never connected. Nil
	// rather than a zero value on purpose: an absent report and a report of
	// "battery empty, no signal" must not look alike.
	Health *DeviceHealth

	// LastSeenAt is nil until the first connection.
	LastSeenAt *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Job is one SMS request.
type Job struct {
	ID        uuid.UUID
	Recipient string
	Body      string
	Mode      JobMode
	// Priority orders the queue; higher is considered first. It is ordering,
	// not preemption — an assigned job is never recalled.
	Priority int

	// ScheduledAt holds the job back until a wall-clock time. Nil means
	// eligible immediately.
	ScheduledAt *time.Time
	// ExpiresAt is the point after which the message must not be sent, so a
	// phone offline for hours cannot deliver a stale OTP on reconnect.
	ExpiresAt *time.Time

	// RequestedDeviceID is the device the caller named, if any. Kept apart from
	// AssignedDeviceID because they answer different questions — what was asked
	// for, versus what happened. A requested device is never substituted.
	RequestedDeviceID *uuid.UUID
	AssignedDeviceID  *uuid.UUID

	Status       JobStatus
	ErrorCode    string
	ErrorMessage string
	// PartsSent is what callers reconcile cost against: a long or non-GSM-7
	// body is split into several billed messages.
	PartsSent int
	// Attempts bounds redispatch. Delivery is at-least-once, so a retry after a
	// reconnect is normal; this stops a job no device can stomach from looping.
	Attempts int

	Callback CallbackState

	CreatedAt   time.Time
	UpdatedAt   time.Time
	AssignedAt  *time.Time
	CompletedAt *time.Time
}

// Expired reports whether the job may no longer be sent as of now.
func (j *Job) Expired(now time.Time) bool {
	return j.ExpiresAt != nil && now.After(*j.ExpiresAt)
}

// CallbackState tracks the outbound webhook for a job. It lives on the job
// rather than in its own table: there is exactly one callback per job, so the
// watcher's poll needs no join.
type CallbackState struct {
	// URL is empty when the caller wants no callback.
	URL      string
	Attempts int
	// NextAttemptAt is when the watcher should try again. Nil means not
	// scheduled — either never attempted, or already delivered.
	NextAttemptAt *time.Time
	DeliveredAt   *time.Time
	LastError     string
}

// Due reports whether the watcher should attempt this callback now.
func (c *CallbackState) Due(now time.Time) bool {
	return c.URL != "" &&
		c.DeliveredAt == nil &&
		c.NextAttemptAt != nil &&
		!now.Before(*c.NextAttemptAt)
}

// EnrollmentToken is a single-use credential that trades for a device token.
type EnrollmentToken struct {
	ID        uuid.UUID
	ExpiresAt time.Time
	// ConsumedAt and DeviceID are set together when the token is redeemed, and
	// are both nil before that.
	ConsumedAt *time.Time
	DeviceID   *uuid.UUID
	CreatedAt  time.Time
}

// Usable reports whether the token can still be redeemed as of now. The
// authoritative check is the atomic consumption in EnrollmentStore.Consume;
// this is for reporting, not for gating.
func (t *EnrollmentToken) Usable(now time.Time) bool {
	return t.ConsumedAt == nil && now.Before(t.ExpiresAt)
}

// JobEvent is one entry in a job's append-only history. It records why a
// message ended up where it did, without complicating the row the scheduler
// reads on every tick.
type JobEvent struct {
	ID    int64
	JobID uuid.UUID
	// Status is the state the job moved into, or empty for an event that is not
	// a transition (a retry, a callback attempt).
	Status JobStatus
	// DeviceID is the device involved, when there is one. Deliberately not a
	// foreign key: the log must outlive any device it mentions.
	DeviceID  *uuid.UUID
	Reason    string
	CreatedAt time.Time
}
