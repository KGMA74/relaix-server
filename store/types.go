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
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/policy"
)

// DispatchCount is how many messages one device, or one of its SIMs, was
// handed over the send-limit windows. SubscriptionID is nil for messages sent
// from the phone's default SIM without naming it.
type DispatchCount struct {
	DeviceID       uuid.UUID
	SubscriptionID *int32
	LastHour       int
	LastDay        int
}

// BucketCount is how many jobs of one status were created in the bucket that
// starts at Start.
type BucketCount struct {
	Start  time.Time
	Status JobStatus
	Count  int
}

// DefaultAccountID is the account seeded by migration 00009. Everything that
// existed before accounts belongs to it, and a deployment that never creates
// another — one operator, one shared API key — works on it alone.
var DefaultAccountID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

// Account owns devices, jobs and enrollment tokens. Deliberately minimal: a
// scope owner. What else an account carries — people, keys, plans — belongs
// to whatever embeds the gateway, keyed on this id.
type Account struct {
	ID        uuid.UUID
	Name      string
	CreatedAt time.Time
}

// Scope narrows a listing to one account, or leaves it across all of them.
// The zero Scope is all accounts: the view of whoever runs the gateway.
type Scope struct {
	AccountID *uuid.UUID
}

// AllAccounts is the unscoped view.
func AllAccounts() Scope { return Scope{} }

// OnlyAccount scopes to one account.
func OnlyAccount(id uuid.UUID) Scope { return Scope{AccountID: &id} }

// Includes reports whether a row owned by account belongs to the scope.
func (s Scope) Includes(account uuid.UUID) bool {
	return s.AccountID == nil || *s.AccountID == account
}

// AccountOrDefault maps the zero id to the default account, so callers
// written before accounts existed — and tests — keep creating rows that
// belong somewhere.
func AccountOrDefault(id uuid.UUID) uuid.UUID {
	if id == uuid.Nil {
		return DefaultAccountID
	}
	return id
}

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

// JobKind is what a job asks a phone to do.
type JobKind string

const (
	// KindSMS sends Body to Recipient. Every job before USSD existed.
	KindSMS JobKind = "sms"
	// KindUSSD dials Body — a USSD code — and records what the network
	// answers. Recipient is empty; Replies answer the menus the code opens.
	KindUSSD JobKind = "ussd"
)

// Capabilities an agent reports beyond sending SMS (DeviceInfo.capabilities).
const (
	// CapUSSD runs a USSD code without replies.
	CapUSSD = "ussd"
	// CapUSSDMenu also answers the menus a code opens.
	CapUSSDMenu = "ussd_menu"
	// CapInboundSMS forwards received SMS.
	CapInboundSMS = "inbound_sms"
	// CapUSSDPin resolves {pin} in a USSD code from the phone's own vault,
	// for codes approved there.
	CapUSSDPin = "ussd_pin"
	// CapAutomationSchedule runs automations on a schedule, not only on
	// received SMS. An agent without it is sent scheduled ones disabled: it
	// would otherwise run them on every SMS.
	CapAutomationSchedule = "automation_schedule"
)

// PinPlaceholder stands for a SIM's PIN in a USSD code. The gateway never
// holds a PIN: the phone substitutes it, and only for codes its owner
// approved on the phone.
const PinPlaceholder = "{pin}"

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

	// Sims is per-SIM signal/readiness/count on a multi-SIM handset. Empty on
	// a single-SIM phone, where the fields above already say it. Keyed by
	// SubscriptionID, refreshed wholesale on every report — there is no
	// per-SIM history, only the latest snapshot.
	Sims []SimHealth
}

// SimHealth is the changing half of a SIM: identity lives in SimInfo, this is
// what a heartbeat refreshes.
type SimHealth struct {
	// SubscriptionID matches a SimInfo.SubscriptionID reported by the same
	// device.
	SubscriptionID int32
	SignalStrength int
	SimReady       bool
	// SentLastHour is per SIM: the carrier meters the SIM, not the handset, so
	// the device-level total above is the sum and the wrong number to rate
	// limit a single SIM against.
	SentLastHour int
}

// SimInfo is the identity of one SIM a handset can send from — stable while
// the SIM stays in the phone, unlike SimHealth.
type SimInfo struct {
	// SubscriptionID is Android's subscription id, and the value
	// Job.SubscriptionID refers to. Deliberately not the slot: slots are
	// reused when a SIM is swapped, which would silently redirect traffic to a
	// different number.
	SubscriptionID int32
	// SlotIndex is the physical slot, 0-based. For an operator's benefit —
	// "the one in the top tray" — never for routing.
	SlotIndex   int32
	PhoneNumber string
	Carrier     string
	DisplayName string
	// IsDefaultSMS marks the subscription used when a job names none. Exactly
	// one SIM has this set.
	IsDefaultSMS bool
}

// Device is an enrolled phone.
type Device struct {
	ID uuid.UUID
	// AccountID owns the device. Zero on Create means the default account.
	AccountID   uuid.UUID
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

	// Sims is every SIM the handset can send from, as of the last Register.
	// Empty on a single-SIM phone, or one that cannot enumerate them: Carrier
	// and PhoneNumber above already describe that case. Refreshed wholesale on
	// Register (a slot changes only on a physical SIM swap, which needs a
	// reconnect anyway), not on every heartbeat.
	Sims []SimInfo

	// HardwareHash is the HMAC of the handset's hardware id, which makes one
	// physical phone one device. Empty for a device enrolled by an agent that
	// does not send one. Never the hardware id itself.
	HardwareHash string

	// TimeZone is the IANA zone the phone reports; the default zone for its
	// availability windows.
	TimeZone string

	// Capabilities is what the agent said it can do beyond sending SMS, as of
	// its last Register (Cap* constants). Empty for an agent that predates it.
	Capabilities []string

	// Policy is when the device takes work and for which recipients. The zero
	// value restricts nothing.
	Policy policy.Policy

	// Health is nil for a device that enrolled but has never connected. Nil
	// rather than a zero value on purpose: an absent report and a report of
	// "battery empty, no signal" must not look alike.
	Health *DeviceHealth

	// LastSeenAt is nil until the first connection.
	LastSeenAt *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Can reports whether the device's agent said it can do capability.
func (d *Device) Can(capability string) bool {
	return slices.Contains(d.Capabilities, capability)
}

// Job is one request to a phone: an SMS to send, or a USSD code to run.
type Job struct {
	ID uuid.UUID
	// AccountID owns the job. Zero on Create means the default account.
	AccountID uuid.UUID
	// Kind is what the phone does. Empty on Create means KindSMS.
	Kind      JobKind
	Recipient string
	// Body is the SMS text, or the USSD code dialed.
	Body string
	Mode JobMode

	// Replies answer, in order, the menus a USSD code opens. Empty: the code
	// alone.
	Replies []string
	// Responses is what the network displayed for a USSD job, one entry per
	// screen.
	Responses []string
	// Sensitive marks a job whose Body or Replies carry a secret (a PIN).
	// Never shown by the API, and wiped from the store once the job is over.
	Sensitive bool
	// Paging follows an answer over several screens; nil: one screen per
	// request. Extract is a pattern whose named groups, found in Responses,
	// are the job's fields.
	Paging  *policy.Paging
	Extract string

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

	// SubscriptionID names which SIM to send from on a multi-SIM handset. Nil
	// means the device's default SMS subscription, which is what a single-SIM
	// phone always uses.
	//
	// Only meaningful together with a device: subscription ids are assigned by
	// Android per handset, so the same number identifies different SIMs on two
	// phones. Naming one without naming a device is refused at the API.
	SubscriptionID *int32

	// AssignedSubscriptionID is the SIM the job was handed to: the requested
	// one, or the one the device's rules picked. Nil while unassigned, or when
	// the phone's default was used without being named. Cleared on release.
	AssignedSubscriptionID *int32

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

// JobFilter narrows JobStore.List. Every field is optional and they combine
// with AND; the zero value matches every job.
type JobFilter struct {
	// AccountID restricts to one account's jobs. Nil: all accounts.
	AccountID *uuid.UUID
	// Kinds matches any of the listed kinds. Empty means all.
	Kinds []JobKind
	// Statuses matches any of the listed states. Empty means all.
	Statuses []JobStatus
	// DeviceID matches a job the device was asked for or was given. Both,
	// because "what did this phone have to do with" is the operator's question,
	// and a requested job not yet placed belongs to the answer.
	DeviceID *uuid.UUID
	// RecipientPrefix matches the start of the E.164 number, so "+237" narrows
	// to a country and a full number to one recipient.
	RecipientPrefix string
	// CreatedFrom is inclusive, CreatedTo exclusive.
	CreatedFrom *time.Time
	CreatedTo   *time.Time

	// After resumes a listing: only jobs strictly older than the cursor, in
	// List's order, are returned. Nil starts from the newest.
	After *JobCursor
	// Limit bounds the result. Zero or less means no bound, which only tests
	// and small fleets should rely on.
	Limit int
}

// JobCursor is a position in List's ordering: the last job a caller has seen.
type JobCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// CursorOf returns the cursor that resumes a listing after j.
func CursorOf(j *Job) *JobCursor {
	return &JobCursor{CreatedAt: j.CreatedAt, ID: j.ID}
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

// InboundMessage is an SMS a phone received and forwarded.
type InboundMessage struct {
	ID        uuid.UUID
	AccountID uuid.UUID
	// DeviceID is the phone it arrived on; nil once that phone is deleted.
	DeviceID *uuid.UUID
	// DeviceRef is the id the agent gave the message, stable across resends.
	DeviceRef string
	// SubscriptionID is the SIM it arrived on, when the phone said.
	SubscriptionID *int32
	Sender         string
	Body           string
	ReceivedAt     time.Time
	CreatedAt      time.Time

	// AutomationID is the automation that forwarded it, and Fields what its
	// pattern extracted. Empty for a message forwarded by the inbound rules.
	AutomationID string
	Fields       map[string]string

	// Webhook tracks the notification of the message, like a job's callback.
	Webhook CallbackState
}

// InboundFilter narrows InboundStore.List; the zero value matches everything.
type InboundFilter struct {
	AccountID *uuid.UUID
	DeviceID  *uuid.UUID
	// Sender matches whole, ignoring case.
	Sender string
	// ReceivedFrom is inclusive, ReceivedTo exclusive.
	ReceivedFrom *time.Time
	ReceivedTo   *time.Time
	// After resumes a listing after the message it names.
	After *InboundCursor
	Limit int
}

// InboundCursor is a position in InboundStore.List's order.
type InboundCursor struct {
	ReceivedAt time.Time
	ID         uuid.UUID
}

// EnrollmentToken is a single-use credential that trades for a device token.
type EnrollmentToken struct {
	ID uuid.UUID
	// AccountID is the account the enrolling device will belong to.
	AccountID uuid.UUID
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
