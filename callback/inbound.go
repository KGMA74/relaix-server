package callback

import (
	"context"
	"errors"
	"time"

	"github.com/KGMA74/relaix-server/store"
)

// InboundSender is the part of *Notifier the inbound watcher uses.
type InboundSender interface {
	NotifyInbound(ctx context.Context, m *store.InboundMessage) error
}

// InboundWatcher posts received SMS to their webhook, retrying like Watcher
// does for job callbacks — same options, same backoff, same reasons.
type InboundWatcher struct {
	store  store.Store
	sender InboundSender
	// jobs lends its options and backoff: one policy for every webhook.
	jobs *Watcher
}

// NewInboundWatcher creates an InboundWatcher.
func NewInboundWatcher(s store.Store, sender InboundSender, opts WatcherOptions) *InboundWatcher {
	opts.withDefaults()
	return &InboundWatcher{store: s, sender: sender, jobs: &Watcher{opts: opts}}
}

// Run polls until ctx is cancelled.
func (w *InboundWatcher) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.jobs.opts.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := w.Tick(ctx); err != nil && !errors.Is(err, context.Canceled) {
				w.jobs.opts.Logger.Error("inbound webhook tick failed", "err", err)
			}
		}
	}
}

// Tick runs one poll: claim in a transaction, post outside it.
func (w *InboundWatcher) Tick(ctx context.Context) error {
	opts := w.jobs.opts
	now := opts.Now()
	var due []*store.InboundMessage
	err := w.store.WithTx(ctx, func(tx store.Store) error {
		var err error
		due, err = tx.Inbound().ClaimWebhooksDue(ctx, now, opts.BatchSize)
		return err
	})
	if err != nil {
		return err
	}
	for _, m := range due {
		if err := ctx.Err(); err != nil {
			return err
		}
		w.deliver(ctx, m, now)
	}
	return nil
}

func (w *InboundWatcher) deliver(ctx context.Context, m *store.InboundMessage, now time.Time) {
	opts := w.jobs.opts
	log := opts.Logger.With("inbound_id", m.ID, "attempt", m.Webhook.Attempts+1)
	inbound := w.store.Inbound()

	err := w.sender.NotifyInbound(ctx, m)
	if err == nil {
		if err := inbound.MarkWebhookDelivered(ctx, m.ID, opts.Now()); err != nil {
			log.Error("inbound webhook delivered but not recorded", "err", err)
		}
		return
	}

	attempts := m.Webhook.Attempts + 1
	var giveUp string
	switch {
	case errors.Is(err, ErrPermanent):
		giveUp = "permanent failure: " + err.Error()
	case attempts >= opts.MaxAttempts:
		giveUp = "gave up after " + itoa(attempts) + " attempts: " + err.Error()
	}
	if giveUp != "" {
		if err := inbound.AbandonWebhook(ctx, m.ID, giveUp); err != nil {
			log.Error("could not record abandoned inbound webhook", "err", err)
		}
		log.Warn("inbound webhook abandoned", "reason", giveUp)
		return
	}
	next := now.Add(w.jobs.backoff(attempts))
	if err := inbound.ScheduleWebhookRetry(ctx, m.ID, next, err.Error()); err != nil {
		log.Error("could not schedule inbound webhook retry", "err", err)
		return
	}
	log.Info("inbound webhook failed, retrying later", "next_attempt_at", next, "err", err)
}
