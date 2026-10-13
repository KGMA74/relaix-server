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

	"github.com/KGMA74/relaix-server/events"
	v1 "github.com/KGMA74/relaix-server/gen/smsgateway/v1"
	"github.com/KGMA74/relaix-server/hub"
	"github.com/KGMA74/relaix-server/policy"
	"github.com/KGMA74/relaix-server/store"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Hub is the part of *hub.Hub the scheduler uses. Narrowed to an interface so
// the scheduler can be tested without running a hub goroutine.
type Hub interface {
	ListReady(ctx context.Context) ([]hub.DeviceState, error)
	SendJob(ctx context.Context, deviceID uuid.UUID, msg *v1.ServerMessage) error
}

// Router decides whose devices may carry an account's messages.
//
// The default, OwnDevices, keeps every account on its own phones. A gateway
// that sends for accounts without phones of their own routes them to the
// devices of another account — a shared pool — by returning it here.
type Router interface {
	// Carriers returns the accounts whose devices may send jobs of account.
	Carriers(ctx context.Context, account uuid.UUID) ([]uuid.UUID, error)
}

// OwnDevices routes each account's jobs to its own devices only.
type OwnDevices struct{}

// Carriers implements Router.
func (OwnDevices) Carriers(_ context.Context, account uuid.UUID) ([]uuid.UUID, error) {
	return []uuid.UUID{account}, nil
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

	// Router picks whose devices may send each account's jobs. Nil keeps
	// every account on its own devices.
	Router Router

	// Events receives job status changes for the dashboard's live stream.
	// Nil publishes nowhere.
	Events *events.Broker

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
	if o.Router == nil {
		o.Router = OwnDevices{}
	}
}

// Scheduler assigns jobs to devices.
type Scheduler struct {
	store store.Store
	hub   Hub
	opts  Options

	// held collects the events of the tick's transaction, published only once
	// it commits: announced earlier, a dashboard refetching on them could read
	// the state from before the change, and on a rollback they would announce
	// a change that never happened. Touched only by Tick, which never runs
	// concurrently with itself.
	held []events.Event

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
	// subscriptionID is the SIM the device's rules picked, or 0 to leave the
	// job's own choice (or the phone's default) alone.
	subscriptionID int32
}

// fleet is the tick's view of every device's rules and of what each was
// handed recently, read once per tick: the hub knows who is ready, only the
// store knows their policies and their send counts.
//
// A job sees the fleet through forAccounts: only the devices allowed to carry
// it. The views share the send counts, so what one job is handed counts
// against the caps every other job sees.
type fleet struct {
	devices map[uuid.UUID]*store.Device
	sent    map[uuid.UUID]policy.Count
	simSent map[uuid.UUID]map[int32]policy.Count
	// all is every device the store knows, whatever the view.
	all map[uuid.UUID]*store.Device
}

func newFleet(devices []*store.Device, counts []store.DispatchCount) fleet {
	f := fleet{
		devices: make(map[uuid.UUID]*store.Device, len(devices)),
		sent:    make(map[uuid.UUID]policy.Count),
		simSent: make(map[uuid.UUID]map[int32]policy.Count),
	}
	for _, d := range devices {
		f.devices[d.ID] = d
	}
	f.all = f.devices
	for _, c := range counts {
		total := f.sent[c.DeviceID]
		total.Hour += c.LastHour
		total.Day += c.LastDay
		f.sent[c.DeviceID] = total
		if c.SubscriptionID != nil {
			f.addSim(c.DeviceID, *c.SubscriptionID, policy.Count{Hour: c.LastHour, Day: c.LastDay})
		}
	}
	return f
}

// forAccounts is the part of the fleet owned by accounts.
func (f fleet) forAccounts(accounts []uuid.UUID) fleet {
	view := f
	view.devices = make(map[uuid.UUID]*store.Device)
	for id, d := range f.all {
		for _, a := range accounts {
			if d.AccountID == a {
				view.devices[id] = d
				break
			}
		}
	}
	return view
}

func (f fleet) addSim(device uuid.UUID, sub int32, n policy.Count) {
	m := f.simSent[device]
	if m == nil {
		m = make(map[int32]policy.Count)
		f.simSent[device] = m
	}
	c := m[sub]
	c.Hour += n.Hour
	c.Day += n.Day
	m[sub] = c
}

// record counts a job handed out during this tick, so that the next job in
// the same tick sees the device's — and SIM's — new total against its caps.
func (f fleet) record(device uuid.UUID, sub int32) {
	c := f.sent[device]
	c.Hour++
	c.Day++
	f.sent[device] = c
	if sub != 0 {
		f.addSim(device, sub, policy.Count{Hour: 1, Day: 1})
	}
}

// policyFor returns a device's rules and what they need to know about it. A
// device the store does not know — gone since the hub listed it — has the
// zero policy, which restricts nothing.
func (f fleet) policyFor(id uuid.UUID) (policy.Policy, policy.Device) {
	d, ok := f.devices[id]
	if !ok {
		return policy.Policy{}, policy.Device{}
	}
	pd := policy.Device{TimeZone: d.TimeZone, Sent: f.sent[id], SimSent: f.simSent[id]}
	// Default SIM first: when several may send, the one the phone's owner
	// chose as default is the least surprising.
	for _, sim := range d.Sims {
		if sim.IsDefaultSMS {
			pd.Sims = append([]int32{sim.SubscriptionID}, pd.Sims...)
		} else {
			pd.Sims = append(pd.Sims, sim.SubscriptionID)
		}
	}
	return d.Policy, pd
}

// enabled reports whether a device may be handed work: it is in the view, and
// turned on. One outside the view — another account's, or one the store no
// longer knows, deleted since the hub listed it — never is: nothing vouches
// for whose messages it may carry.
func (f fleet) enabled(id uuid.UUID) bool {
	d, ok := f.devices[id]
	return ok && d.Enabled
}

// anyAcceptsEver reports whether some enabled device's rules would ever take a
// job for recipient. An empty fleet answers yes: a phone not yet enrolled may
// still come, and the job keeps waiting for it as it always did.
func (f fleet) anyAcceptsEver(recipient string) bool {
	enabled := 0
	for id, d := range f.devices {
		if !d.Enabled {
			continue
		}
		enabled++
		p, pd := f.policyFor(id)
		if p.AcceptsEver(recipient, pd, 0) {
			return true
		}
	}
	return enabled == 0
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
	all, err := s.store.Devices().List(ctx, store.AllAccounts())
	if err != nil {
		return err
	}
	counts, err := s.store.Jobs().CountDispatched(ctx, now.Add(-time.Hour), now.Add(-24*time.Hour))
	if err != nil {
		return err
	}
	devices := newFleet(all, counts)

	var pending []dispatch
	s.held = s.held[:0]
	err = s.store.WithTx(ctx, func(tx store.Store) error {
		jobs, err := tx.Jobs().ClaimSchedulable(ctx, now, s.opts.BatchSize)
		if err != nil {
			return err
		}

		// Devices claimed earlier in this same tick, so one pass does not hand
		// every job to the same phone.
		used := make(map[uuid.UUID]int, len(ready))
		views := make(map[uuid.UUID]fleet)

		for _, job := range jobs {
			view, ok := views[job.AccountID]
			if !ok {
				carriers, err := s.opts.Router.Carriers(ctx, job.AccountID)
				if err != nil {
					// Left pending, not failed: the next tick asks again.
					s.opts.Logger.Warn("could not route account", "account_id", job.AccountID, "err", err)
					continue
				}
				view = devices.forAccounts(carriers)
				views[job.AccountID] = view
			}
			d, sub, ok := s.decide(ctx, tx, job, ready, view, used, now)
			if !ok {
				continue
			}
			// The SIM the job goes out on: the rules' pick, else the one the
			// caller named, else the phone's default (0).
			if sub == 0 && job.SubscriptionID != nil {
				sub = *job.SubscriptionID
			}
			if err := tx.Jobs().MarkAssigned(ctx, job.ID, d, sub, now); err != nil {
				// Lost a race with a cancellation or another instance. Not
				// fatal: skip this job and keep placing the rest.
				if errors.Is(err, store.ErrConflict) {
					s.opts.Logger.Debug("job no longer assignable", "job_id", job.ID)
					continue
				}
				return err
			}
			used[d]++
			devices.record(d, sub)
			s.event(ctx, tx, job, store.JobAssigned, &d, "assigned by scheduler")
			pending = append(pending, dispatch{job: job, deviceID: d, subscriptionID: sub})
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, e := range s.held {
		s.opts.Events.Publish(e)
	}

	s.push(ctx, pending, now)
	return nil
}

// decide picks a device — and, when its rules care, a SIM — for one job, or
// resolves the job if it cannot be sent at all. It reports whether a device
// was found.
//
// Device rules split refusals in two. A recipient no rule will ever accept
// fails the job at once: waiting would only delay the same answer. A device
// outside its hours is "not now", and a queued job waits for it.
func (s *Scheduler) decide(
	ctx context.Context,
	tx store.Store,
	job *store.Job,
	ready []hub.DeviceState,
	devices fleet,
	used map[uuid.UUID]int,
	now time.Time,
) (uuid.UUID, int32, bool) {
	// Expiry beats everything: a job past its deadline must not be sent even if
	// a device is free. Late delivery of a time-sensitive message is worse than
	// none at all.
	if job.Expired(now) {
		s.fail(ctx, tx, job, "expired", "job expired before a device was available", now)
		return uuid.Nil, 0, false
	}

	if job.Attempts >= s.opts.MaxAttempts {
		s.fail(ctx, tx, job, "too_many_attempts", "exceeded maximum dispatch attempts", now)
		return uuid.Nil, 0, false
	}

	if job.Kind == store.KindUSSD {
		return s.decideUSSD(ctx, tx, job, ready, devices, now)
	}

	// Explicit selection. A caller naming a device usually means a specific
	// SIM, number or carrier, so it is never silently rerouted: substituting
	// another device would be wrong in a way the caller cannot detect.
	if job.RequestedDeviceID != nil {
		want := *job.RequestedDeviceID
		var requestedSub int32
		if job.SubscriptionID != nil {
			requestedSub = *job.SubscriptionID
		}
		if _, mine := devices.devices[want]; !mine {
			if _, known := devices.all[want]; known {
				// Never "not now": the device will not come to serve this
				// account by waiting.
				s.fail(ctx, tx, job, "device_not_allowed",
					"the requested device does not send for this account", now)
				return uuid.Nil, 0, false
			}
		}
		p, pd := devices.policyFor(want)
		if !devices.enabled(want) {
			// Disabled since the hub listed it: not now, like an offline device.
			if job.Mode == store.ModeImmediate {
				s.fail(ctx, tx, job, "device_unavailable", "requested device is disabled and mode is immediate", now)
			}
			return uuid.Nil, 0, false
		}
		choice := p.Choose(now, job.Recipient, pd, requestedSub)
		if choice.Never {
			s.fail(ctx, tx, job, "rejected_by_device_rules",
				"the requested device's rules do not accept this recipient", now)
			return uuid.Nil, 0, false
		}
		if choice.OK {
			for _, d := range ready {
				if d.DeviceID == want {
					return want, choice.SubscriptionID, true
				}
			}
		}
		if job.Mode == store.ModeImmediate {
			reason := "requested device is not ready and mode is immediate"
			if !choice.OK {
				reason = "requested device is outside its availability hours and mode is immediate"
			}
			s.fail(ctx, tx, job, "device_unavailable", reason, now)
		}
		// Queued: leave it pending and try again next tick.
		return uuid.Nil, 0, false
	}

	if !devices.anyAcceptsEver(job.Recipient) {
		s.fail(ctx, tx, job, "no_device_accepts",
			"no device's rules accept this recipient", now)
		return uuid.Nil, 0, false
	}

	// Automatic selection among the ready devices whose rules take this job
	// now, spreading load across them.
	eligible := make([]hub.DeviceState, 0, len(ready))
	subs := make(map[uuid.UUID]int32, len(ready))
	for _, d := range ready {
		// A device can still be connected for a moment after being disabled;
		// it is never handed work in that moment.
		if !devices.enabled(d.DeviceID) {
			continue
		}
		p, pd := devices.policyFor(d.DeviceID)
		if c := p.Choose(now, job.Recipient, pd, 0); c.OK {
			eligible = append(eligible, d)
			subs[d.DeviceID] = c.SubscriptionID
		}
	}
	if d, ok := s.pick(eligible, used); ok {
		return d, subs[d], true
	}

	if job.Mode == store.ModeImmediate {
		s.fail(ctx, tx, job, "no_device_available",
			"no ready device whose rules accept this recipient now, and mode is immediate", now)
	}
	return uuid.Nil, 0, false
}

// decideUSSD places a USSD job. It always names its device — a code runs
// against one SIM's account, and another phone's answer would be about
// another account — so this is the explicit-selection path of decide, minus
// what only concerns SMS: recipient rules and send limits. The device's
// availability hours still apply: they say when its owner lets it work.
func (s *Scheduler) decideUSSD(
	ctx context.Context,
	tx store.Store,
	job *store.Job,
	ready []hub.DeviceState,
	devices fleet,
	now time.Time,
) (uuid.UUID, int32, bool) {
	if job.RequestedDeviceID == nil {
		s.fail(ctx, tx, job, "device_required", "a USSD job must name its device", now)
		return uuid.Nil, 0, false
	}
	want := *job.RequestedDeviceID
	d, mine := devices.devices[want]
	if !mine {
		if _, known := devices.all[want]; known {
			s.fail(ctx, tx, job, "device_not_allowed",
				"the requested device does not send for this account", now)
			return uuid.Nil, 0, false
		}
	}
	// An agent that does not list the capability would drop the job without a
	// word. Failed rather than kept waiting: it takes an update of the app,
	// not time.
	if d != nil {
		need := store.CapUSSD
		if len(job.Replies) > 0 {
			need = store.CapUSSDMenu
		}
		if !d.Can(need) {
			s.fail(ctx, tx, job, "unsupported_by_device",
				"the requested device's agent cannot run this USSD job ("+need+"); update the app", now)
			return uuid.Nil, 0, false
		}
	}

	var sub int32
	if job.SubscriptionID != nil {
		sub = *job.SubscriptionID
	}
	open := d == nil || d.Policy.Availability.Open(now, d.TimeZone)
	if devices.enabled(want) && open {
		for _, r := range ready {
			if r.DeviceID == want {
				return want, sub, true
			}
		}
	}
	if job.Mode == store.ModeImmediate {
		reason := "requested device is not ready and mode is immediate"
		switch {
		case !devices.enabled(want):
			reason = "requested device is disabled and mode is immediate"
		case !open:
			reason = "requested device is outside its availability hours and mode is immediate"
		}
		s.fail(ctx, tx, job, "device_unavailable", reason, now)
	}
	return uuid.Nil, 0, false
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
				SendSmsJob: toProto(p.job, p.subscriptionID),
			},
		}
		if p.job.Kind == store.KindUSSD {
			msg.Payload = &v1.ServerMessage_UssdJob{UssdJob: toUSSDProto(p.job, p.subscriptionID)}
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
		s.event(ctx, s.store, p.job, store.JobPending, &p.deviceID, reason)
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
	s.event(ctx, tx, job, store.JobFailed, nil, reason)
}

// event appends to the audit trail. A failure here never fails the operation
// being audited: losing a log line is bad, losing an SMS is worse.
func (s *Scheduler) event(ctx context.Context, st store.Store, job *store.Job, status store.JobStatus, deviceID *uuid.UUID, reason string) {
	jobID := job.ID
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

	ev := events.Event{Type: events.JobUpdated, JobID: jobID.String(), Status: string(status), AccountID: job.AccountID}
	if deviceID != nil {
		ev.DeviceID = deviceID.String()
	}
	if st == s.store {
		s.opts.Events.Publish(ev)
	} else {
		// Inside the tick's transaction: held until it commits.
		s.held = append(s.held, ev)
	}
}

// toProto converts a job to the message pushed to a device. sub, when
// non-zero, is the SIM the device's rules chose, and overrides the job's.
func toProto(j *store.Job, sub int32) *v1.SendSmsJob {
	msg := &v1.SendSmsJob{
		JobId:     j.ID.String(),
		Recipient: j.Recipient,
		Body:      j.Body,
		Priority:  int32(j.Priority),
	}
	if j.ExpiresAt != nil {
		msg.ExpiresAt = timestamppb.New(*j.ExpiresAt)
	}
	if j.SubscriptionID != nil {
		msg.SubscriptionId = *j.SubscriptionID
	}
	if sub != 0 {
		msg.SubscriptionId = sub
	}
	// Left at zero otherwise, which the contract defines as "the handset's
	// default SMS subscription" — the behaviour every job had before the field
	// existed.
	return msg
}

// toUSSDProto converts a USSD job to the message pushed to a device.
func toUSSDProto(j *store.Job, sub int32) *v1.UssdJob {
	msg := &v1.UssdJob{
		JobId:          j.ID.String(),
		Code:           j.Body,
		SubscriptionId: sub,
		Replies:        j.Replies,
	}
	if j.ExpiresAt != nil {
		msg.ExpiresAt = timestamppb.New(*j.ExpiresAt)
	}
	return msg
}
