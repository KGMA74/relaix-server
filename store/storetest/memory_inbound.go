package storetest

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/store"
)

// Inbound implements store.Store.
func (s *Store) Inbound() store.InboundStore { return inboundStore{s} }

var _ store.InboundStore = inboundStore{}

type inboundStore struct{ s *Store }

func (i inboundStore) Record(ctx context.Context, in *store.InboundMessage) (*store.InboundMessage, bool, error) {
	defer i.s.lock()()
	if err := i.s.takeFailure(); err != nil {
		return nil, false, err
	}
	// Mirrors the unique index over (device_id, device_ref).
	for _, m := range i.s.inbound {
		if m.DeviceID != nil && in.DeviceID != nil && *m.DeviceID == *in.DeviceID && m.DeviceRef == in.DeviceRef {
			return copyInbound(m), false, nil
		}
	}
	cp := copyInbound(in)
	if cp.ID == uuid.Nil {
		cp.ID = uuid.New()
	}
	cp.AccountID = store.AccountOrDefault(cp.AccountID)
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now()
	}
	i.s.inbound[cp.ID] = cp
	return copyInbound(cp), true, nil
}

func (i inboundStore) Get(ctx context.Context, id uuid.UUID) (*store.InboundMessage, error) {
	defer i.s.lock()()
	m, ok := i.s.inbound[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return copyInbound(m), nil
}

func (i inboundStore) List(ctx context.Context, f store.InboundFilter) ([]*store.InboundMessage, error) {
	defer i.s.lock()()
	if err := i.s.takeFailure(); err != nil {
		return nil, err
	}
	var out []*store.InboundMessage
	for _, m := range i.s.inbound {
		if inboundMatches(m, f) {
			out = append(out, copyInbound(m))
		}
	}
	slices.SortFunc(out, func(a, b *store.InboundMessage) int {
		return -compareInboundOrder(a.ReceivedAt, a.ID, b.ReceivedAt, b.ID)
	})
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

func inboundMatches(m *store.InboundMessage, f store.InboundFilter) bool {
	switch {
	case f.AccountID != nil && m.AccountID != *f.AccountID:
		return false
	case f.DeviceID != nil && (m.DeviceID == nil || *m.DeviceID != *f.DeviceID):
		return false
	case f.Sender != "" && !strings.EqualFold(m.Sender, f.Sender):
		return false
	case f.ReceivedFrom != nil && m.ReceivedAt.Before(*f.ReceivedFrom):
		return false
	case f.ReceivedTo != nil && !m.ReceivedAt.Before(*f.ReceivedTo):
		return false
	case f.After != nil && compareInboundOrder(m.ReceivedAt, m.ID, f.After.ReceivedAt, f.After.ID) >= 0:
		return false
	}
	return true
}

func compareInboundOrder(aAt time.Time, aID uuid.UUID, bAt time.Time, bID uuid.UUID) int {
	if c := aAt.Compare(bAt); c != 0 {
		return c
	}
	return bytes.Compare(aID[:], bID[:])
}

func (i inboundStore) ClaimWebhooksDue(ctx context.Context, now time.Time, limit int) ([]*store.InboundMessage, error) {
	defer i.s.lock()()
	if err := i.s.takeFailure(); err != nil {
		return nil, err
	}
	var out []*store.InboundMessage
	for _, m := range i.s.inbound {
		if m.Webhook.Due(now) {
			out = append(out, copyInbound(m))
		}
	}
	slices.SortFunc(out, func(a, b *store.InboundMessage) int {
		return a.Webhook.NextAttemptAt.Compare(*b.Webhook.NextAttemptAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (i inboundStore) MarkWebhookDelivered(ctx context.Context, id uuid.UUID, at time.Time) error {
	return i.update(id, func(m *store.InboundMessage) {
		m.Webhook.DeliveredAt = &at
		m.Webhook.NextAttemptAt = nil
	})
}

func (i inboundStore) ScheduleWebhookRetry(ctx context.Context, id uuid.UUID, nextAt time.Time, lastErr string) error {
	return i.update(id, func(m *store.InboundMessage) {
		m.Webhook.Attempts++
		m.Webhook.NextAttemptAt = &nextAt
		m.Webhook.LastError = lastErr
	})
}

func (i inboundStore) AbandonWebhook(ctx context.Context, id uuid.UUID, reason string) error {
	return i.update(id, func(m *store.InboundMessage) {
		m.Webhook.Attempts++
		m.Webhook.NextAttemptAt = nil
		m.Webhook.LastError = reason
	})
}

func (i inboundStore) update(id uuid.UUID, fn func(*store.InboundMessage)) error {
	defer i.s.lock()()
	if err := i.s.takeFailure(); err != nil {
		return err
	}
	m, ok := i.s.inbound[id]
	if !ok {
		return store.ErrNotFound
	}
	fn(m)
	return nil
}

func copyInbound(m *store.InboundMessage) *store.InboundMessage {
	if m == nil {
		return nil
	}
	cp := *m
	cp.DeviceID = copyUUID(m.DeviceID)
	if m.SubscriptionID != nil {
		sub := *m.SubscriptionID
		cp.SubscriptionID = &sub
	}
	cp.Webhook.NextAttemptAt = copyTime(m.Webhook.NextAttemptAt)
	cp.Webhook.DeliveredAt = copyTime(m.Webhook.DeliveredAt)
	return &cp
}
