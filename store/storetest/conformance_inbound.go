package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/store"
)

func testInbound(t *testing.T, newStore Factory) {
	ctx := context.Background()
	base := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)

	device := func(t *testing.T, s store.Store, account uuid.UUID, hash string) uuid.UUID {
		t.Helper()
		d, err := s.Devices().Create(ctx, &store.Device{Label: hash, AccountID: account}, hash)
		if err != nil {
			t.Fatalf("Create device: %v", err)
		}
		return d.ID
	}
	record := func(t *testing.T, s store.Store, dev uuid.UUID, ref, sender string, at time.Time) (*store.InboundMessage, bool) {
		t.Helper()
		d, err := s.Devices().Get(ctx, dev)
		if err != nil {
			t.Fatalf("Get device: %v", err)
		}
		m, created, err := s.Inbound().Record(ctx, &store.InboundMessage{
			AccountID: d.AccountID, DeviceID: &dev, DeviceRef: ref, Sender: sender, Body: "Vous avez recu 5000 FCFA", ReceivedAt: at,
			SubscriptionID: new(int32(1)),
		})
		if err != nil {
			t.Fatalf("Record: %v", err)
		}
		return m, created
	}

	t.Run("record is once per device and ref", func(t *testing.T) {
		s := newStore(t)
		dev := device(t, s, store.DefaultAccountID, "in-1")
		first, created := record(t, s, dev, "ref-1", "OrangeMoney", base)
		if !created || first.ID == uuid.Nil || first.AccountID != store.DefaultAccountID {
			t.Fatalf("first record = %+v, created %v", first, created)
		}
		again, created := record(t, s, dev, "ref-1", "OrangeMoney", base)
		if created || again.ID != first.ID {
			t.Errorf("a resend created %v (id %v, want %v)", created, again.ID, first.ID)
		}
		// The same ref from another phone is another message.
		other := device(t, s, store.DefaultAccountID, "in-2")
		if _, created := record(t, s, other, "ref-1", "OrangeMoney", base); !created {
			t.Error("another device's message with the same ref was taken for a resend")
		}

		got, err := s.Inbound().Get(ctx, first.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.AutomationID != "" || len(got.Fields) != 0 {
			t.Errorf("a plain message has automation %q, fields %v", got.AutomationID, got.Fields)
		}
		withFields, _, err := s.Inbound().Record(ctx, &store.InboundMessage{
			DeviceID: &dev, DeviceRef: "ref-fields", Sender: "OrangeMoney", Body: "x", ReceivedAt: base,
			AutomationID: "auto-1", Fields: map[string]string{"amount": "5 000", "ref": "CI1"},
		})
		if err != nil {
			t.Fatalf("Record with fields: %v", err)
		}
		if back, _ := s.Inbound().Get(ctx, withFields.ID); back.AutomationID != "auto-1" || back.Fields["amount"] != "5 000" || back.Fields["ref"] != "CI1" {
			t.Errorf("fields lost: %+v", back)
		}
		if got.Sender != "OrangeMoney" || got.SubscriptionID == nil || *got.SubscriptionID != 1 || !got.ReceivedAt.Equal(base) {
			t.Errorf("round trip lost fields: %+v", got)
		}
		if _, err := s.Inbound().Get(ctx, uuid.New()); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Get(unknown) = %v, want ErrNotFound", err)
		}
	})

	t.Run("list is newest received first, filters and pages", func(t *testing.T) {
		s := newStore(t)
		acc, err := s.Accounts().Create(ctx, "other")
		if err != nil {
			t.Fatalf("Create account: %v", err)
		}
		dev := device(t, s, store.DefaultAccountID, "in-3")
		theirs := device(t, s, acc.ID, "in-4")
		for i := range 5 {
			sender := "OrangeMoney"
			if i%2 == 1 {
				sender = "MoovMoney"
			}
			record(t, s, dev, "r"+string(rune('a'+i)), sender, base.Add(time.Duration(i)*time.Minute))
		}
		record(t, s, theirs, "x", "OrangeMoney", base)

		mine := store.DefaultAccountID
		all, err := s.Inbound().List(ctx, store.InboundFilter{AccountID: &mine})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(all) != 5 || !all[0].ReceivedAt.Equal(base.Add(4*time.Minute)) {
			t.Fatalf("List = %d messages, first at %v", len(all), all[0].ReceivedAt)
		}

		orange, _ := s.Inbound().List(ctx, store.InboundFilter{AccountID: &mine, Sender: "orangemoney"})
		if len(orange) != 3 {
			t.Errorf("sender filter = %d, want 3 (case-insensitive)", len(orange))
		}
		from, to := base.Add(time.Minute), base.Add(3*time.Minute)
		window, _ := s.Inbound().List(ctx, store.InboundFilter{AccountID: &mine, ReceivedFrom: &from, ReceivedTo: &to})
		if len(window) != 2 {
			t.Errorf("window = %d, want 2", len(window))
		}

		page1, _ := s.Inbound().List(ctx, store.InboundFilter{AccountID: &mine, Limit: 3})
		last := page1[len(page1)-1]
		page2, _ := s.Inbound().List(ctx, store.InboundFilter{AccountID: &mine, Limit: 3,
			After: &store.InboundCursor{ReceivedAt: last.ReceivedAt, ID: last.ID}})
		if len(page1) != 3 || len(page2) != 2 || page2[0].ReceivedAt.After(last.ReceivedAt) {
			t.Errorf("pages = %d + %d", len(page1), len(page2))
		}
	})

	t.Run("webhook lifecycle", func(t *testing.T) {
		s := newStore(t)
		dev := device(t, s, store.DefaultAccountID, "in-5")
		due := base
		m, _, err := s.Inbound().Record(ctx, &store.InboundMessage{
			DeviceID: &dev, DeviceRef: "w", Sender: "OrangeMoney", Body: "x", ReceivedAt: base,
			Webhook: store.CallbackState{URL: "https://example.com/hook", NextAttemptAt: &due},
		})
		if err != nil {
			t.Fatalf("Record: %v", err)
		}
		if _, _, err := s.Inbound().Record(ctx, &store.InboundMessage{DeviceID: &dev, DeviceRef: "silent", Sender: "a", Body: "b", ReceivedAt: base}); err != nil {
			t.Fatalf("Record without webhook: %v", err)
		}

		claimed, err := s.Inbound().ClaimWebhooksDue(ctx, base.Add(time.Second), 10)
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if len(claimed) != 1 || claimed[0].ID != m.ID {
			t.Fatalf("claimed %d, want the one with a URL", len(claimed))
		}
		next := base.Add(time.Minute)
		if err := s.Inbound().ScheduleWebhookRetry(ctx, m.ID, next, "503"); err != nil {
			t.Fatalf("Retry: %v", err)
		}
		if c, _ := s.Inbound().ClaimWebhooksDue(ctx, base.Add(time.Second), 10); len(c) != 0 {
			t.Error("claimed before its retry time")
		}
		if err := s.Inbound().MarkWebhookDelivered(ctx, m.ID, next); err != nil {
			t.Fatalf("Delivered: %v", err)
		}
		got, _ := s.Inbound().Get(ctx, m.ID)
		if got.Webhook.DeliveredAt == nil || got.Webhook.Attempts != 1 || got.Webhook.LastError != "503" {
			t.Errorf("webhook = %+v", got.Webhook)
		}
		if c, _ := s.Inbound().ClaimWebhooksDue(ctx, next.Add(time.Hour), 10); len(c) != 0 {
			t.Error("claimed after delivery")
		}
		if err := s.Inbound().AbandonWebhook(ctx, uuid.New(), "x"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Abandon(unknown) = %v, want ErrNotFound", err)
		}
	})

	t.Run("messages outlive their device", func(t *testing.T) {
		s := newStore(t)
		dev := device(t, s, store.DefaultAccountID, "in-6")
		m, _ := record(t, s, dev, "kept", "OrangeMoney", base)
		if err := s.Devices().Delete(ctx, dev); err != nil {
			t.Fatalf("Delete device: %v", err)
		}
		got, err := s.Inbound().Get(ctx, m.ID)
		if err != nil {
			t.Fatalf("Get after delete: %v", err)
		}
		if got.DeviceID != nil {
			t.Errorf("DeviceID = %v, want nil once the phone is gone", got.DeviceID)
		}
	})
}
