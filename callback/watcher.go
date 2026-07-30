package callback

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"math/rand/v2"
	"time"

	"github.com/KGMA74/relaix-server/store"
)

// Sender is the part of *Notifier the watcher uses.
type Sender interface {
	Notify(ctx context.Context, job *store.Job) error
}

// WatcherOptions configures a Watcher. The zero value is usable.
type WatcherOptions struct {
	// Interval between polls. Default 5s. Callbacks are not latency-critical
	// the way an SMS is — the caller already got its job id synchronously — so
	// this is deliberately slower than the scheduler's tick.
	Interval time.Duration

	// BatchSize bounds one poll. Default 32.
	BatchSize int

	// BaseDelay is the wait after the first failure. Default 30s.
	BaseDelay time.Duration

	// MaxDelay caps the backoff. Default 1h: past that, retrying more often
	// buys nothing against an outage measured in hours.
	MaxDelay time.Duration

	// MaxAttempts is when to give up. Default 10, which with the default delays
	// spans roughly a day before a callback is abandoned.
	MaxAttempts int

	// Jitter is the fraction of the delay randomised, from 0 to 1. Default
	// 0.2. Without it, a fleet-wide outage produces a synchronised retry
	// stampede the moment the receiver comes back — every callback that failed
	// together retries together, forever.
	Jitter float64

	Now    func() time.Time
	Logger *slog.Logger

	// rand is injectable so tests can pin the jitter.
	rand func() float64
}

func (o *WatcherOptions) withDefaults() {
	if o.Interval <= 0 {
		o.Interval = 5 * time.Second
	}
	if o.BatchSize <= 0 {
		o.BatchSize = 32
	}
	if o.BaseDelay <= 0 {
		o.BaseDelay = 30 * time.Second
	}
	if o.MaxDelay <= 0 {
		o.MaxDelay = time.Hour
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 10
	}
	if o.Jitter < 0 || o.Jitter > 1 {
		o.Jitter = 0.2
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.rand == nil {
		o.rand = rand.Float64
	}
}

// Watcher retries callbacks the receiver has not accepted yet.
//
// It exists because a receiver will be down sometimes, and losing a delivery
// report because of a five-minute outage would make callbacks something callers
// cannot rely on — at which point they poll instead, and the whole mechanism
// has failed at its purpose.
type Watcher struct {
	store  store.Store
	sender Sender
	opts   WatcherOptions
}

// NewWatcher creates a Watcher.
func NewWatcher(s store.Store, sender Sender, opts WatcherOptions) *Watcher {
	opts.withDefaults()
	return &Watcher{store: s, sender: sender, opts: opts}
}

// Run polls until ctx is cancelled. A failing poll is logged and the loop
// continues: the next pass re-derives everything from the database anyway.
func (w *Watcher) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.opts.Interval)
	defer ticker.Stop()

	w.opts.Logger.Info("callback watcher started",
		"interval", w.opts.Interval, "max_attempts", w.opts.MaxAttempts)

	for {
		select {
		case <-ctx.Done():
			w.opts.Logger.Info("callback watcher stopped")
			return nil
		case <-ticker.C:
			if err := w.Tick(ctx); err != nil && !errors.Is(err, context.Canceled) {
				w.opts.Logger.Error("callback tick failed", "err", err)
			}
		}
	}
}

// Tick runs one poll. Exported so tests can drive it without a timer.
//
// The claim happens inside a transaction that ends before any HTTP is
// attempted. Holding row locks across a network call to somebody else's server
// would mean one unresponsive receiver pins a database connection for the
// timeout — and with several of them, the pool is gone.
func (w *Watcher) Tick(ctx context.Context) error {
	now := w.opts.Now()

	var due []*store.Job
	err := w.store.WithTx(ctx, func(tx store.Store) error {
		var err error
		due, err = tx.Jobs().ClaimCallbacksDue(ctx, now, w.opts.BatchSize)
		return err
	})
	if err != nil {
		return err
	}

	for _, job := range due {
		if err := ctx.Err(); err != nil {
			return err
		}
		w.deliver(ctx, job, now)
	}
	return nil
}

// deliver attempts one callback and records what happened.
func (w *Watcher) deliver(ctx context.Context, job *store.Job, now time.Time) {
	log := w.opts.Logger.With("job_id", job.ID, "attempt", job.Callback.Attempts+1)

	err := w.sender.Notify(ctx, job)
	if err == nil {
		if err := w.store.Jobs().MarkCallbackDelivered(ctx, job.ID, w.opts.Now()); err != nil {
			log.Error("callback delivered but not recorded", "err", err)
			return
		}
		w.event(ctx, job, "callback delivered")
		log.Info("callback delivered")
		return
	}

	attempts := job.Callback.Attempts + 1

	// Two ways to stop: the receiver rejected us outright, or we have tried
	// long enough. Both are given up on the same way — no next attempt — so the
	// claim query never returns the job again.
	if errors.Is(err, ErrPermanent) {
		w.giveUp(ctx, job, "permanent failure: "+err.Error())
		log.Warn("callback abandoned, receiver rejected it", "err", err)
		return
	}
	if attempts >= w.opts.MaxAttempts {
		w.giveUp(ctx, job, "gave up after "+itoa(attempts)+" attempts: "+err.Error())
		log.Warn("callback abandoned, out of attempts", "err", err)
		return
	}

	next := now.Add(w.backoff(attempts))
	if err := w.store.Jobs().ScheduleCallbackRetry(ctx, job.ID, next, err.Error()); err != nil {
		log.Error("could not schedule callback retry", "err", err)
		return
	}
	log.Info("callback failed, retrying later", "next_attempt_at", next, "err", err)
}

// giveUp stops retrying, leaving the failure recorded for inspection.
//
// It does not mark the callback delivered. That would be a lie, and it would
// make "which callbacks actually reached the caller" unanswerable — which is
// precisely the question asked after an outage.
func (w *Watcher) giveUp(ctx context.Context, job *store.Job, reason string) {
	if err := w.store.Jobs().AbandonCallback(ctx, job.ID, reason); err != nil {
		w.opts.Logger.Error("could not record abandoned callback", "job_id", job.ID, "err", err)
		return
	}
	w.event(ctx, job, reason)
}

// backoff returns the wait before attempt n+1, doubling each time, capped, then
// jittered.
//
// Doubling rather than a fixed interval so a receiver down for an hour is not
// hammered thousands of times; jittered so a fleet-wide outage does not produce
// a synchronised stampede when it recovers.
func (w *Watcher) backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}

	// Cap the exponent before shifting: at attempt 64 the shift would overflow
	// and hand back a negative delay, which would schedule the retry in the
	// past and spin.
	exp := min(attempt-1, 32)
	delay := float64(w.opts.BaseDelay) * math.Pow(2, float64(exp))

	if delay > float64(w.opts.MaxDelay) {
		delay = float64(w.opts.MaxDelay)
	}

	// Jitter only downwards, so the cap stays a cap.
	if w.opts.Jitter > 0 {
		delay *= 1 - w.opts.Jitter*w.opts.rand()
	}
	return time.Duration(delay)
}

func (w *Watcher) event(ctx context.Context, job *store.Job, reason string) {
	e := &store.JobEvent{
		JobID:     job.ID,
		Reason:    reason,
		CreatedAt: w.opts.Now(),
	}
	if err := w.store.Events().Append(ctx, e); err != nil {
		w.opts.Logger.Warn("could not record callback event", "job_id", job.ID, "err", err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
