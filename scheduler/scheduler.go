// Package scheduler pairs pending jobs with devices able to send them.
//
// It is a tick loop, not an event-driven dispatcher. On each tick it claims a
// batch of eligible jobs in priority order, asks the hub which devices are
// ready, decides who sends what, and pushes the work down the streams the
// devices already hold open. Polling rather than reacting is deliberate: work
// becomes eligible for reasons no event announces — a scheduled_at falling due,
// a device coming back after an outage, a released job — so a loop that
// re-derives the whole picture each pass has no missed-wakeup failure mode.
//
// See docs/architecture.md §5 in the monorepo.
package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	v1 "github.com/KGMA74/relaix-server/gen/smsgateway/v1"
	"github.com/KGMA74/relaix-server/hub"
	"github.com/KGMA74/relaix-server/store"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Hub is the part of *hub.Hub the scheduler uses. Narrowed to an interface so
// the scheduler can be tested without running a hub goroutine.
type Hub interface {
	ListReady(ctx context.Context) ([]hub.DeviceState, error)
	SendJob(ctx context.Context, deviceID uuid.UUID, msg *v1.ServerMessage) error
}

// Options configures a Scheduler. The zero value is usable.
type Options struct {
	// Interval between ticks. Default 1s: short enough that an SMS submitted
	// through the API feels immediate, long enough that an idle fleet is not
	// hammering the database.
	Interval time.Duration

	// BatchSize bounds how many jobs one tick claims. Default 64. It exists to
	// bound the transaction, not the throughput: a backlog is drained over
	// several ticks rather than in one long-held lock.
	BatchSize int

	// MaxAttempts is how many times a job may be dispatched before it is
	// failed. Default 5. Delivery is at-least-once, so redispatch after a
	// reconnect is normal; this bounds a job no device can stomach.
	MaxAttempts int

	Now    func() time.Time
	Logger *slog.Logger
}

func (o *Options) withDefaults() {
	if o.Interval <= 0 {
		o.Interval = time.Second
	}
	if o.BatchSize <= 0 {
		o.BatchSize = 64
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 5
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
}

// Scheduler assigns jobs to devices.
type Scheduler struct {
	store store.Store
	hub   Hub
	opts  Options

	// rr rotates the starting point in the ready list from tick to tick, so
	// load spreads across the fleet instead of always landing on whichever
	// device the hub happened to list first. Per-device throughput is the
	// binding constraint, so spreading is the whole game.
	rr int
}

// New creates a Scheduler.
func New(s store.Store, h Hub, opts Options) *Scheduler {
	opts.withDefaults()
	return &Scheduler{store: s, hub: h, opts: opts}
}

// Run ticks until ctx is cancelled. A tick that fails is logged and the loop
// continues: a transient database error must not take the scheduler down, and
// the next pass re-derives everything anyway.
func (s *Scheduler) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.opts.Interval)
	defer ticker.Stop()

	s.opts.Logger.Info("scheduler started",
		"interval", s.opts.Interval, "batch", s.opts.BatchSize)

	for {
		select {
		case <-ctx.Done():
			s.opts.Logger.Info("scheduler stopped")
			return nil
		case <-ticker.C:
			if err := s.Tick(ctx); err != nil && !errors.Is(err, context.Canceled) {
				s.opts.Logger.Error("scheduler tick failed", "err", err)
			}
		}
	}
}

// dispatch is one decision made inside the transaction and acted on after it
// commits.
type dispatch struct {
	job      *store.Job
	deviceID uuid.UUID
}

// Tick runs one pass. Exported so tests can drive it directly instead of
// waiting on a timer.
//
// The ordering here is the load-bearing part. Everything that touches the
// database happens inside one transaction; the pushes to the hub happen after
// it commits. Sending first would risk a rollback leaving a device holding a
// job the database still calls pending — a duplicate SMS on the next tick.
// Committing first means a crash before the push leaves a job marked assigned
// that nobody holds, which is the recoverable direction: the agent reports what
// it actually has on reconnect and the job is released.
func (s *Scheduler) Tick(ctx context.Context) error {
	now := s.opts.Now()

	ready, err := s.hub.ListReady(ctx)
	if err != nil {
		return err
	}

	var pending []dispatch
	err = s.store.WithTx(ctx, func(tx store.Store) error {
		jobs, err := tx.Jobs().ClaimSchedulable(ctx, now, s.opts.BatchSize)
		if err != nil {
			return err
		}

		// Devices claimed earlier in this same tick, so one pass does not hand
		// every job to the same phone.
		used := make(map[uuid.UUID]int, len(ready))

		for _, job := range jobs {
			d, ok := s.decide(ctx, tx, job, ready, used, now)
			if !ok {
				continue
			}
			if err := tx.Jobs().MarkAssigned(ctx, job.ID, d, now); err != nil {
				// Lost a race with a cancellation or another instance. Not
				// fatal: skip this job and keep placing the rest.
				if errors.Is(err, store.ErrConflict) {
					s.opts.Logger.Debug("job no longer assignable", "job_id", job.ID)
					continue
				}
				return err
			}
			used[d]++
			s.event(ctx, tx, job.ID, store.JobAssigned, &d, "assigned by scheduler")
			pending = append(pending, dispatch{job: job, deviceID: d})
		}
		return nil
	})
	if err != nil {
		return err
	}

	s.push(ctx, pending, now)
	return nil
}

// decide picks a device for one job, or resolves the job if it cannot be sent
// at all. It reports whether a device was found.
func (s *Scheduler) decide(
	ctx context.Context,
	tx store.Store,
	job *store.Job,
	ready []hub.DeviceState,
	used map[uuid.UUID]int,
	now time.Time,
) (uuid.UUID, bool) {
	// Expiry beats everything: a job past its deadline must not be sent even if
	// a device is free. Late delivery of a time-sensitive message is worse than
	// none at all.
	if job.Expired(now) {
		s.fail(ctx, tx, job, "expired", "job expired before a device was available", now)
		return uuid.Nil, false
	}

	if job.Attempts >= s.opts.MaxAttempts {
		s.fail(ctx, tx, job, "too_many_attempts", "exceeded maximum dispatch attempts", now)
		return uuid.Nil, false
	}

	// Explicit selection. A caller naming a device usually means a specific
	// SIM, number or carrier, so it is never silently rerouted: substituting
	// another device would be wrong in a way the caller cannot detect.
	if job.RequestedDeviceID != nil {
		want := *job.RequestedDeviceID
		for _, d := range ready {
			if d.DeviceID == want {
				return want, true
			}
		}
		if job.Mode == store.ModeImmediate {
			s.fail(ctx, tx, job, "device_unavailable",
				"requested device is not ready and mode is immediate", now)
		}
		// Queued: leave it pending and try again next tick.
		return uuid.Nil, false
	}

	// Automatic selection, spreading load across the fleet.
	if d, ok := s.pick(ready, used); ok {
		return d, true
	}

	if job.Mode == store.ModeImmediate {
		s.fail(ctx, tx, job, "no_device_available",
			"no ready device and mode is immediate", now)
	}
	return uuid.Nil, false
}

// pick chooses the least-loaded ready device, starting from a rotating offset
// so that ties do not always resolve to the same phone.
func (s *Scheduler) pick(ready []hub.DeviceState, used map[uuid.UUID]int) (uuid.UUID, bool) {
	if len(ready) == 0 {
		return uuid.Nil, false
	}

	best, bestLoad := uuid.Nil, -1
	for i := range ready {
		d := ready[(s.rr+i)%len(ready)]
		load := used[d.DeviceID]
		if bestLoad == -1 || load < bestLoad {
			best, bestLoad = d.DeviceID, load
		}
	}
	s.rr++
	return best, true
}

// push delivers the committed assignments to the hub. A device that has gone
// away or stopped reading between the commit and now gets its job released, so
// the next tick can place it elsewhere rather than leaving it stuck assigned to
// a phone that never received it.
func (s *Scheduler) push(ctx context.Context, pending []dispatch, now time.Time) {
	for _, p := range pending {
		msg := &v1.ServerMessage{
			MessageId: uuid.NewString(),
			SentAt:    timestamppb.New(now),
			Payload: &v1.ServerMessage_SendSmsJob{
				SendSmsJob: toProto(p.job),
			},
		}

		err := s.hub.SendJob(ctx, p.deviceID, msg)
		if err == nil {
			continue
		}

		reason := "device unreachable at dispatch: " + err.Error()
		s.opts.Logger.Warn("dispatch failed, releasing job",
			"job_id", p.job.ID, "device_id", p.deviceID, "err", err)

		if relErr := s.store.Jobs().Release(ctx, p.job.ID, reason); relErr != nil {
			// The job stays assigned. Reconciliation on the device's next
			// Register will catch it; log loudly rather than retry blindly.
			s.opts.Logger.Error("could not release undelivered job",
				"job_id", p.job.ID, "err", relErr)
			continue
		}
		s.event(ctx, s.store, p.job.ID, store.JobPending, &p.deviceID, reason)
	}
}

// fail resolves a job that can never be sent.
func (s *Scheduler) fail(ctx context.Context, tx store.Store, job *store.Job, code, reason string, now time.Time) {
	err := tx.Jobs().Complete(ctx, job.ID, store.JobResult{
		Status:       store.JobFailed,
		ErrorCode:    code,
		ErrorMessage: reason,
		CompletedAt:  now,
	})
	if err != nil {
		s.opts.Logger.Error("could not fail job", "job_id", job.ID, "err", err)
		return
	}
	s.event(ctx, tx, job.ID, store.JobFailed, nil, reason)
}

// event appends to the audit trail. A failure here never fails the operation
// being audited: losing a log line is bad, losing an SMS is worse.
func (s *Scheduler) event(ctx context.Context, st store.Store, jobID uuid.UUID, status store.JobStatus, deviceID *uuid.UUID, reason string) {
	e := &store.JobEvent{
		JobID:     jobID,
		Status:    status,
		DeviceID:  deviceID,
		Reason:    reason,
		CreatedAt: s.opts.Now(),
	}
	if err := st.Events().Append(ctx, e); err != nil {
		s.opts.Logger.Warn("could not record job event", "job_id", jobID, "err", err)
	}
}

// toProto converts a job to the message pushed to a device.
func toProto(j *store.Job) *v1.SendSmsJob {
	msg := &v1.SendSmsJob{
		JobId:     j.ID.String(),
		Recipient: j.Recipient,
		Body:      j.Body,
		Priority:  int32(j.Priority),
	}
	if j.ExpiresAt != nil {
		msg.ExpiresAt = timestamppb.New(*j.ExpiresAt)
	}
	return msg
}
