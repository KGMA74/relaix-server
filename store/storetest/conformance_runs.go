package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/KGMA74/relaix-server/store"
)

func testRuns(t *testing.T, newStore Factory) {
	ctx := context.Background()
	base := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)

	t.Run("record once per device and ref, list newest first and filter", func(t *testing.T) {
		s := newStore(t)
		d, err := s.Devices().Create(ctx, &store.Device{Label: "r"}, "hash-runs")
		if err != nil {
			t.Fatalf("Create device: %v", err)
		}
		record := func(ref, automation string, at time.Time, ok bool) (*store.AutomationRun, bool) {
			t.Helper()
			r, created, err := s.AutomationRuns().Record(ctx, &store.AutomationRun{
				DeviceID: &d.ID, DeviceRef: ref, AutomationID: automation, Sender: "OrangeMoney",
				StartedAt: at, FinishedAt: at.Add(time.Second), OK: ok,
				Steps: []store.AutomationStep{{Action: "ussd", OK: ok, Detail: "*144*{pin}#", Responses: []string{"Transfert reussi"}}},
			})
			if err != nil {
				t.Fatalf("Record: %v", err)
			}
			return r, created
		}
		first, created := record("a", "pay", base, true)
		if !created || first.AccountID != store.DefaultAccountID {
			t.Fatalf("first = %+v, created %v", first, created)
		}
		if again, created := record("a", "pay", base, true); created || again.ID != first.ID {
			t.Errorf("a resend created a second run")
		}
		record("b", "pay", base.Add(time.Minute), false)
		record("c", "other", base.Add(2*time.Minute), true)

		all, err := s.AutomationRuns().List(ctx, store.RunFilter{DeviceID: &d.ID})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(all) != 3 || all[0].DeviceRef != "c" || all[2].DeviceRef != "a" {
			t.Fatalf("order = %v", refs(all))
		}
		if got := all[2]; len(got.Steps) != 1 || got.Steps[0].Detail != "*144*{pin}#" || got.Steps[0].Responses[0] != "Transfert reussi" {
			t.Errorf("steps lost: %+v", got.Steps)
		}
		pay, _ := s.AutomationRuns().List(ctx, store.RunFilter{AutomationID: "pay", Limit: 1})
		if len(pay) != 1 || pay[0].DeviceRef != "b" {
			t.Errorf("filtered = %v", refs(pay))
		}
	})
}

func refs(runs []*store.AutomationRun) []string {
	out := make([]string, len(runs))
	for i, r := range runs {
		out[i] = r.DeviceRef
	}
	return out
}
