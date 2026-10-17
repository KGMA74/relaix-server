package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/policy"
	"github.com/KGMA74/relaix-server/store"
)

// Factory builds an empty Store for one test. Implementations that talk to a
// database should return one scoped to a clean schema, since the suite assumes
// it starts from nothing.
type Factory func(t *testing.T) store.Store

// RunConformance runs the store contract against an implementation.
//
// It exists because the rest of the test suite leans on the in-memory fake, and
// a fake is only worth anything if it behaves like the thing it stands in for.
// Running one body of tests against both is what keeps them from drifting: a
// behaviour the fake invents, or a rule Postgres enforces that the fake does
// not, shows up here rather than in production.
//
// It deliberately checks the contract the interfaces promise — error kinds,
// ordering, atomicity, idempotency — and not storage details like index use.
func RunConformance(t *testing.T, newStore Factory) {
	t.Helper()

	t.Run("Accounts", func(t *testing.T) { testAccounts(t, newStore) })
	t.Run("Devices", func(t *testing.T) { testDevices(t, newStore) })
	t.Run("Jobs", func(t *testing.T) { testJobs(t, newStore) })
	t.Run("Enrollment", func(t *testing.T) { testEnrollment(t, newStore) })
	t.Run("Events", func(t *testing.T) { testEvents(t, newStore) })
	t.Run("Transactions", func(t *testing.T) { testTransactions(t, newStore) })
	t.Run("USSD", func(t *testing.T) { testUSSD(t, newStore) })
	t.Run("Inbound", func(t *testing.T) { testInbound(t, newStore) })
	t.Run("AutomationRuns", func(t *testing.T) { testRuns(t, newStore) })
}

func testDevices(t *testing.T, newStore Factory) {
	ctx := context.Background()

	t.Run("create and read back", func(t *testing.T) {
		s := newStore(t)
		created, err := s.Devices().Create(ctx, &store.Device{
			Label: "desk", PhoneNumber: "+33600000000", Model: "SM-A536B",
		}, "hash-1")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if created.ID == uuid.Nil {
			t.Fatal("Create returned no id")
		}
		if !created.Enabled {
			t.Error("a new device must be enabled")
		}
		// A device that has never connected has no health, and that must not be
		// reported as a zeroed struct.
		if created.Health != nil {
			t.Errorf("Health = %+v, want nil for a device that never connected", created.Health)
		}

		got, err := s.Devices().Get(ctx, created.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Label != "desk" || got.Model != "SM-A536B" {
			t.Errorf("round trip lost fields: %+v", got)
		}
	})

	t.Run("get unknown is ErrNotFound", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.Devices().Get(ctx, uuid.New()); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("Get = %v, want ErrNotFound", err)
		}
	})

	t.Run("token hash resolves the device", func(t *testing.T) {
		s := newStore(t)
		created, err := s.Devices().Create(ctx, &store.Device{Label: "a"}, "hash-tok")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := s.Devices().GetByTokenHash(ctx, "hash-tok")
		if err != nil {
			t.Fatalf("GetByTokenHash: %v", err)
		}
		if got.ID != created.ID {
			t.Errorf("resolved %v, want %v", got.ID, created.ID)
		}
		if _, err := s.Devices().GetByTokenHash(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("unknown hash = %v, want ErrNotFound", err)
		}
	})

	t.Run("duplicate token hash is ErrConflict", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.Devices().Create(ctx, &store.Device{Label: "a"}, "same"); err != nil {
			t.Fatalf("first Create: %v", err)
		}
		if _, err := s.Devices().Create(ctx, &store.Device{Label: "b"}, "same"); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("second Create = %v, want ErrConflict", err)
		}
	})

	t.Run("hardware hash finds the device and is unique when set", func(t *testing.T) {
		s := newStore(t)
		created, err := s.Devices().Create(ctx, &store.Device{Label: "a", HardwareHash: "hw-1"}, "tok-a")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := s.Devices().GetByHardwareHash(ctx, "hw-1")
		if err != nil {
			t.Fatalf("GetByHardwareHash: %v", err)
		}
		if got.ID != created.ID || got.HardwareHash != "hw-1" {
			t.Errorf("resolved %+v, want device %v with its hash", got, created.ID)
		}
		if _, err := s.Devices().GetByHardwareHash(ctx, "other"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("unknown hash = %v, want ErrNotFound", err)
		}
		if _, err := s.Devices().Create(ctx, &store.Device{Label: "b", HardwareHash: "hw-1"}, "tok-b"); !errors.Is(err, store.ErrConflict) {
			t.Errorf("second device with the same hash = %v, want ErrConflict", err)
		}

		// Devices from older agents carry no hash, and any number of them
		// must coexist: the index is unique only where the hash is set.
		for _, tok := range []string{"tok-c", "tok-d"} {
			if _, err := s.Devices().Create(ctx, &store.Device{Label: "old"}, tok); err != nil {
				t.Fatalf("Create without hash: %v", err)
			}
		}
		if _, err := s.Devices().GetByHardwareHash(ctx, ""); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("empty hash = %v, want ErrNotFound", err)
		}
	})

	t.Run("rekey swaps the token, refreshes and re-enables the same device", func(t *testing.T) {
		s := newStore(t)
		created, err := s.Devices().Create(ctx, &store.Device{Label: "old", Model: "A1", HardwareHash: "hw"}, "tok-old")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := s.Devices().SetEnabled(ctx, created.ID, false); err != nil {
			t.Fatalf("SetEnabled: %v", err)
		}

		got, err := s.Devices().Rekey(ctx, created.ID, &store.Device{Label: "new", Model: "A2"}, "tok-new")
		if err != nil {
			t.Fatalf("Rekey: %v", err)
		}
		if got.ID != created.ID || got.Label != "new" || got.Model != "A2" || !got.Enabled {
			t.Errorf("after Rekey: %+v", got)
		}
		if got.HardwareHash != "hw" {
			t.Errorf("Rekey dropped the hardware hash: %q", got.HardwareHash)
		}
		if _, err := s.Devices().GetByTokenHash(ctx, "tok-old"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("old token after Rekey = %v, want ErrNotFound", err)
		}
		if d, err := s.Devices().GetByTokenHash(ctx, "tok-new"); err != nil || d.ID != created.ID {
			t.Errorf("new token = %v, %v; want device %v", d, err, created.ID)
		}
		if _, err := s.Devices().Rekey(ctx, uuid.New(), &store.Device{}, "tok-x"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Rekey unknown = %v, want ErrNotFound", err)
		}
	})

	t.Run("policy round-trips and versions, time zone is kept", func(t *testing.T) {
		s := newStore(t)
		created, err := s.Devices().Create(ctx, &store.Device{Label: "a", TimeZone: "Africa/Ouagadougou"}, "tok-p")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if created.Policy.Version != 0 || len(created.Policy.Accept.Countries) != 0 {
			t.Errorf("new device policy = %+v, want the zero policy", created.Policy)
		}
		if created.TimeZone != "Africa/Ouagadougou" {
			t.Errorf("TimeZone = %q", created.TimeZone)
		}

		want := policy.Policy{
			Availability: policy.Availability{Windows: []policy.Window{{Days: []int{1, 2}, Start: 480, End: 1200}}},
			Accept:       policy.AcceptRules{Countries: []string{"BF"}, Pattern: `^\+226`},
			Sims:         []policy.SimPolicy{{SubscriptionID: 2, Disabled: true}},
		}
		first, err := s.Devices().SetPolicy(ctx, created.ID, want)
		if err != nil {
			t.Fatalf("SetPolicy: %v", err)
		}
		if first.Version != 1 || first.UpdatedAt == nil {
			t.Errorf("first SetPolicy: version %d, updatedAt %v", first.Version, first.UpdatedAt)
		}
		second, err := s.Devices().SetPolicy(ctx, created.ID, want)
		if err != nil {
			t.Fatalf("second SetPolicy: %v", err)
		}
		if second.Version != 2 {
			t.Errorf("second version = %d, want 2", second.Version)
		}

		got, err := s.Devices().Get(ctx, created.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		p := got.Policy
		if p.Version != 2 || p.Accept.Pattern != `^\+226` || len(p.Accept.Countries) != 1 ||
			len(p.Availability.Windows) != 1 || p.Availability.Windows[0].End != 1200 ||
			len(p.Sims) != 1 || !p.Sims[0].Disabled {
			t.Errorf("stored policy = %+v", p)
		}

		if err := s.Devices().UpdateInfo(ctx, created.ID, &store.Device{Label: "a", TimeZone: "Europe/Paris"}); err != nil {
			t.Fatalf("UpdateInfo: %v", err)
		}
		got, _ = s.Devices().Get(ctx, created.ID)
		if got.TimeZone != "Europe/Paris" {
			t.Errorf("TimeZone after UpdateInfo = %q", got.TimeZone)
		}
		if got.Policy.Version != 2 {
			t.Error("UpdateInfo touched the policy")
		}

		if _, err := s.Devices().SetPolicy(ctx, uuid.New(), want); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("SetPolicy unknown = %v, want ErrNotFound", err)
		}
	})

	t.Run("touch records health and liveness", func(t *testing.T) {
		s := newStore(t)
		dev, err := s.Devices().Create(ctx, &store.Device{Label: "a"}, "hash-touch")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		seen := time.Now().UTC().Truncate(time.Millisecond)
		health := &store.DeviceHealth{
			BatteryLevel: 55, IsCharging: true, SignalStrength: 3,
			NetworkType: "LTE", SimReady: true, SentLastHour: 7,
			PermissionsOK: true, ReportedAt: seen,
			Sims: []store.SimHealth{
				{SubscriptionID: 1, SignalStrength: 4, SimReady: true, SentLastHour: 5},
				{SubscriptionID: 2, SignalStrength: 2, SimReady: false, SentLastHour: 2},
			},
		}
		if err := s.Devices().Touch(ctx, dev.ID, health, seen); err != nil {
			t.Fatalf("Touch: %v", err)
		}

		got, err := s.Devices().Get(ctx, dev.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Health == nil {
			t.Fatal("Health is nil after Touch")
		}
		if got.Health.BatteryLevel != 55 || got.Health.SignalStrength != 3 ||
			!got.Health.IsCharging || !got.Health.SimReady ||
			got.Health.SentLastHour != 7 || !got.Health.PermissionsOK ||
			got.Health.NetworkType != "LTE" {
			t.Errorf("health round trip lost fields: %+v", got.Health)
		}
		if len(got.Health.Sims) != 2 || got.Health.Sims[0] != health.Sims[0] ||
			got.Health.Sims[1] != health.Sims[1] {
			t.Errorf("sim health round trip lost fields: %+v", got.Health.Sims)
		}
		if got.LastSeenAt == nil {
			t.Error("LastSeenAt is nil after Touch")
		}

		// A second Touch with no Sims must not be read as "the device lost its
		// second SIM": a nil health leaves the row untouched altogether, which
		// this test already exercises for the scalar fields above, and it must
		// hold for Sims too.
		if err := s.Devices().Touch(ctx, dev.ID, nil, seen.Add(time.Second)); err != nil {
			t.Fatalf("Touch(nil): %v", err)
		}
		got, err = s.Devices().Get(ctx, dev.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if len(got.Health.Sims) != 2 {
			t.Errorf("Touch(nil) changed Sims: %+v", got.Health.Sims)
		}
	})

	t.Run("touch unknown is ErrNotFound", func(t *testing.T) {
		s := newStore(t)
		err := s.Devices().Touch(ctx, uuid.New(), nil, time.Now())
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("Touch = %v, want ErrNotFound", err)
		}
	})

	t.Run("update info and enabled", func(t *testing.T) {
		s := newStore(t)
		dev, err := s.Devices().Create(ctx, &store.Device{Label: "old"}, "hash-upd")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		sims := []store.SimInfo{
			{SubscriptionID: 1, SlotIndex: 0, PhoneNumber: "+33600000001",
				Carrier: "Orange", DisplayName: "Perso", IsDefaultSMS: true},
			{SubscriptionID: 2, SlotIndex: 1, Carrier: "SFR", DisplayName: "Work"},
		}
		if err := s.Devices().UpdateInfo(ctx, dev.ID, &store.Device{
			Label: "new", PhoneNumber: "+1", Carrier: "Orange", Sims: sims,
		}); err != nil {
			t.Fatalf("UpdateInfo: %v", err)
		}
		if err := s.Devices().SetEnabled(ctx, dev.ID, false); err != nil {
			t.Fatalf("SetEnabled: %v", err)
		}

		got, err := s.Devices().Get(ctx, dev.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Label != "new" || got.Carrier != "Orange" {
			t.Errorf("UpdateInfo did not take: %+v", got)
		}
		if len(got.Sims) != 2 || got.Sims[0] != sims[0] || got.Sims[1] != sims[1] {
			t.Errorf("sim info round trip lost fields: %+v", got.Sims)
		}
		if got.Enabled {
			t.Error("SetEnabled(false) did not take")
		}

		// A re-enrollment with no SIMs (single-SIM phone, or one that cannot
		// enumerate them) must clear the previous list rather than leave it
		// stale — unlike Touch, UpdateInfo always replaces.
		if err := s.Devices().UpdateInfo(ctx, dev.ID, &store.Device{Label: "new"}); err != nil {
			t.Fatalf("UpdateInfo (clear sims): %v", err)
		}
		got, err = s.Devices().Get(ctx, dev.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if len(got.Sims) != 0 {
			t.Errorf("UpdateInfo with no sims left %d behind, want 0", len(got.Sims))
		}
	})

	t.Run("list returns everything", func(t *testing.T) {
		s := newStore(t)
		for i, hash := range []string{"h1", "h2", "h3"} {
			if _, err := s.Devices().Create(ctx, &store.Device{Label: string(rune('a' + i))}, hash); err != nil {
				t.Fatalf("Create: %v", err)
			}
		}
		got, err := s.Devices().List(ctx, store.AllAccounts())
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 3 {
			t.Errorf("List returned %d devices, want 3", len(got))
		}
	})

	t.Run("delete removes the device", func(t *testing.T) {
		s := newStore(t)
		dev, err := s.Devices().Create(ctx, &store.Device{Label: "retired"}, "hash-delete")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		if err := s.Devices().Delete(ctx, dev.ID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := s.Devices().Get(ctx, dev.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Get after Delete = %v, want ErrNotFound", err)
		}

		// The token must stop authenticating the instant the row is gone, or a
		// retired handset would keep holding a stream.
		if _, err := s.Devices().GetByTokenHash(ctx, "hash-delete"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetByTokenHash after Delete = %v, want ErrNotFound", err)
		}
	})

	t.Run("delete is not found twice", func(t *testing.T) {
		s := newStore(t)
		dev, err := s.Devices().Create(ctx, &store.Device{Label: "gone"}, "hash-twice")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := s.Devices().Delete(ctx, dev.ID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if err := s.Devices().Delete(ctx, dev.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("second Delete = %v, want ErrNotFound", err)
		}
	})

	t.Run("delete works on a device that consumed an enrollment token", func(t *testing.T) {
		// The realistic case, and the one that broke in production while the
		// earlier delete tests passed: every real device got here by consuming
		// a token, and enrollment_tokens carried a CHECK that made
		// "consumed by a device that no longer exists" unrepresentable.
		s := newStore(t)
		const tokenHash = "hash-enrolled-delete"
		if _, err := s.Enrollments().Create(ctx, uuid.Nil, tokenHash, time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("Enrollments.Create: %v", err)
		}
		dev, err := s.Devices().Create(ctx, &store.Device{Label: "enrolled"}, "hash-dev-enrolled")
		if err != nil {
			t.Fatalf("Devices.Create: %v", err)
		}
		if _, err := s.Enrollments().Consume(ctx, tokenHash, dev.ID, time.Now()); err != nil {
			t.Fatalf("Consume: %v", err)
		}

		if err := s.Devices().Delete(ctx, dev.ID); err != nil {
			t.Fatalf("Delete after enrollment: %v", err)
		}
	})

	t.Run("delete keeps the jobs the device sent", func(t *testing.T) {
		s := newStore(t)
		dev, err := s.Devices().Create(ctx, &store.Device{Label: "sender"}, "hash-jobs")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		job, err := s.Jobs().Create(ctx, &store.Job{
			Recipient:         "+22600000000",
			Body:              "kept",
			Mode:              store.ModeQueued,
			RequestedDeviceID: &dev.ID,
		})
		if err != nil {
			t.Fatalf("Jobs.Create: %v", err)
		}
		if err := s.Jobs().MarkAssigned(ctx, job.ID, dev.ID, 0, time.Now()); err != nil {
			t.Fatalf("MarkAssigned: %v", err)
		}

		if err := s.Devices().Delete(ctx, dev.ID); err != nil {
			t.Fatalf("Delete: %v", err)
		}

		// Retiring a handset must not erase the record of its work: the row
		// survives with its device columns nulled, which is what the schema's
		// ON DELETE SET NULL buys.
		got, err := s.Jobs().Get(ctx, job.ID)
		if err != nil {
			t.Fatalf("Jobs.Get after device Delete: %v", err)
		}
		if got.AssignedDeviceID != nil {
			t.Errorf("AssignedDeviceID = %v, want nil after the device was deleted", got.AssignedDeviceID)
		}
		if got.RequestedDeviceID != nil {
			t.Errorf("RequestedDeviceID = %v, want nil after the device was deleted", got.RequestedDeviceID)
		}
	})
}

func testJobs(t *testing.T, newStore Factory) {
	ctx := context.Background()

	newJob := func(t *testing.T, s store.Store, j *store.Job) *store.Job {
		t.Helper()
		if j.Mode == "" {
			j.Mode = store.ModeQueued
		}
		if j.Recipient == "" {
			j.Recipient = "+33600000000"
		}
		if j.Body == "" {
			j.Body = "hello"
		}
		created, err := s.Jobs().Create(ctx, j)
		if err != nil {
			t.Fatalf("Create job: %v", err)
		}
		return created
	}

	t.Run("create starts pending", func(t *testing.T) {
		s := newStore(t)
		job := newJob(t, s, &store.Job{Priority: 3})
		if job.Status != store.JobPending {
			t.Errorf("status = %q, want %q", job.Status, store.JobPending)
		}
		if job.Attempts != 0 {
			t.Errorf("attempts = %d, want 0", job.Attempts)
		}

		got, err := s.Jobs().Get(ctx, job.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Priority != 3 || got.Mode != store.ModeQueued {
			t.Errorf("round trip lost fields: %+v", got)
		}
	})

	t.Run("get unknown is ErrNotFound", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.Jobs().Get(ctx, uuid.New()); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("Get = %v, want ErrNotFound", err)
		}
	})

	t.Run("claim orders by priority", func(t *testing.T) {
		s := newStore(t)
		now := time.Now()
		newJob(t, s, &store.Job{Priority: 1, Body: "low"})
		newJob(t, s, &store.Job{Priority: 9, Body: "high"})
		newJob(t, s, &store.Job{Priority: 5, Body: "mid"})

		var claimed []*store.Job
		err := s.WithTx(ctx, func(tx store.Store) error {
			var err error
			claimed, err = tx.Jobs().ClaimSchedulable(ctx, now, 10)
			return err
		})
		if err != nil {
			t.Fatalf("ClaimSchedulable: %v", err)
		}
		if len(claimed) != 3 {
			t.Fatalf("claimed %d jobs, want 3", len(claimed))
		}
		if claimed[0].Body != "high" || claimed[2].Body != "low" {
			t.Errorf("order = %q,%q,%q; want high,mid,low",
				claimed[0].Body, claimed[1].Body, claimed[2].Body)
		}
	})

	t.Run("claim respects scheduled_at and limit", func(t *testing.T) {
		s := newStore(t)
		now := time.Now()
		later := now.Add(time.Hour)
		newJob(t, s, &store.Job{Body: "now"})
		newJob(t, s, &store.Job{Body: "later", ScheduledAt: &later})

		var claimed []*store.Job
		if err := s.WithTx(ctx, func(tx store.Store) error {
			var err error
			claimed, err = tx.Jobs().ClaimSchedulable(ctx, now, 10)
			return err
		}); err != nil {
			t.Fatalf("ClaimSchedulable: %v", err)
		}
		if len(claimed) != 1 || claimed[0].Body != "now" {
			t.Fatalf("claimed %d jobs, want just the due one", len(claimed))
		}

		if err := s.WithTx(ctx, func(tx store.Store) error {
			var err error
			claimed, err = tx.Jobs().ClaimSchedulable(ctx, later.Add(time.Minute), 1)
			return err
		}); err != nil {
			t.Fatalf("ClaimSchedulable: %v", err)
		}
		if len(claimed) != 1 {
			t.Errorf("limit ignored: claimed %d, want 1", len(claimed))
		}
	})

	t.Run("assign then release", func(t *testing.T) {
		s := newStore(t)
		dev, err := s.Devices().Create(ctx, &store.Device{Label: "d"}, "hash-job")
		if err != nil {
			t.Fatalf("Create device: %v", err)
		}
		job := newJob(t, s, &store.Job{})

		at := time.Now().UTC().Truncate(time.Millisecond)
		if err := s.Jobs().MarkAssigned(ctx, job.ID, dev.ID, 0, at); err != nil {
			t.Fatalf("MarkAssigned: %v", err)
		}

		got, _ := s.Jobs().Get(ctx, job.ID)
		if got.Status != store.JobAssigned {
			t.Errorf("status = %q, want %q", got.Status, store.JobAssigned)
		}
		if got.AssignedDeviceID == nil || *got.AssignedDeviceID != dev.ID {
			t.Errorf("assigned device = %v, want %v", got.AssignedDeviceID, dev.ID)
		}
		if got.Attempts != 1 {
			t.Errorf("attempts = %d, want 1", got.Attempts)
		}

		// Assigning twice must fail: the second caller lost the race.
		if err := s.Jobs().MarkAssigned(ctx, job.ID, dev.ID, 0, at); !errors.Is(err, store.ErrConflict) {
			t.Errorf("second MarkAssigned = %v, want ErrConflict", err)
		}

		if err := s.Jobs().Release(ctx, job.ID, "device gone"); err != nil {
			t.Fatalf("Release: %v", err)
		}
		got, _ = s.Jobs().Get(ctx, job.ID)
		if got.Status != store.JobPending {
			t.Errorf("status after release = %q, want %q", got.Status, store.JobPending)
		}
		if got.AssignedDeviceID != nil {
			t.Errorf("assigned device = %v after release, want nil", got.AssignedDeviceID)
		}
		// Attempts is not rewound: it bounds how often we try, not how often we
		// succeed.
		if got.Attempts != 1 {
			t.Errorf("attempts = %d after release, want 1", got.Attempts)
		}
	})

	t.Run("assign unknown job is ErrNotFound", func(t *testing.T) {
		s := newStore(t)
		err := s.Jobs().MarkAssigned(ctx, uuid.New(), uuid.New(), 0, time.Now())
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("MarkAssigned = %v, want ErrNotFound", err)
		}
	})

	t.Run("complete is idempotent and allows late delivered", func(t *testing.T) {
		s := newStore(t)
		job := newJob(t, s, &store.Job{})
		at := time.Now().UTC().Truncate(time.Millisecond)

		sent := store.JobResult{Status: store.JobSent, PartsSent: 2, CompletedAt: at}
		if err := s.Jobs().Complete(ctx, job.ID, sent); err != nil {
			t.Fatalf("Complete(sent): %v", err)
		}
		// Results are at-least-once, so a repeat must be a no-op, not an error.
		if err := s.Jobs().Complete(ctx, job.ID, sent); err != nil {
			t.Fatalf("duplicate Complete(sent) = %v, want nil", err)
		}
		// A late delivery report is a real transition and must be accepted.
		if err := s.Jobs().Complete(ctx, job.ID, store.JobResult{
			Status: store.JobDelivered, CompletedAt: at,
		}); err != nil {
			t.Fatalf("Complete(delivered) after sent = %v, want nil", err)
		}

		got, _ := s.Jobs().Get(ctx, job.ID)
		if got.Status != store.JobDelivered {
			t.Errorf("status = %q, want %q", got.Status, store.JobDelivered)
		}
		if got.PartsSent != 2 {
			t.Errorf("parts_sent = %d, want 2", got.PartsSent)
		}

		// Going backwards is refused.
		if err := s.Jobs().Complete(ctx, job.ID, store.JobResult{
			Status: store.JobFailed, CompletedAt: at,
		}); !errors.Is(err, store.ErrConflict) {
			t.Errorf("Complete(failed) after delivered = %v, want ErrConflict", err)
		}
	})

	t.Run("cancel refuses once terminal", func(t *testing.T) {
		s := newStore(t)
		job := newJob(t, s, &store.Job{})
		if err := s.Jobs().Cancel(ctx, job.ID, "user asked"); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
		got, _ := s.Jobs().Get(ctx, job.ID)
		if got.Status != store.JobCancelled {
			t.Errorf("status = %q, want %q", got.Status, store.JobCancelled)
		}

		// There is no recalling a message the handset already sent.
		sentJob := newJob(t, s, &store.Job{})
		if err := s.Jobs().Complete(ctx, sentJob.ID, store.JobResult{
			Status: store.JobSent, CompletedAt: time.Now(),
		}); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if err := s.Jobs().Cancel(ctx, sentJob.ID, "too late"); !errors.Is(err, store.ErrConflict) {
			t.Errorf("Cancel after sent = %v, want ErrConflict", err)
		}
	})

	t.Run("list assigned to device", func(t *testing.T) {
		s := newStore(t)
		dev, err := s.Devices().Create(ctx, &store.Device{Label: "d"}, "hash-list")
		if err != nil {
			t.Fatalf("Create device: %v", err)
		}
		mine := newJob(t, s, &store.Job{Body: "mine"})
		newJob(t, s, &store.Job{Body: "unassigned"})

		if err := s.Jobs().MarkAssigned(ctx, mine.ID, dev.ID, 0, time.Now()); err != nil {
			t.Fatalf("MarkAssigned: %v", err)
		}

		got, err := s.Jobs().ListAssignedTo(ctx, dev.ID)
		if err != nil {
			t.Fatalf("ListAssignedTo: %v", err)
		}
		if len(got) != 1 || got[0].ID != mine.ID {
			t.Errorf("ListAssignedTo returned %d jobs, want just the assigned one", len(got))
		}
	})

	t.Run("callback lifecycle", func(t *testing.T) {
		s := newStore(t)
		now := time.Now().UTC().Truncate(time.Millisecond)
		due := now.Add(-time.Minute)

		job := newJob(t, s, &store.Job{
			Callback: store.CallbackState{URL: "https://example.test/cb", NextAttemptAt: &due},
		})
		// A job with no callback URL must never be picked up.
		newJob(t, s, &store.Job{Body: "no callback"})

		var claimed []*store.Job
		if err := s.WithTx(ctx, func(tx store.Store) error {
			var err error
			claimed, err = tx.Jobs().ClaimCallbacksDue(ctx, now, 10)
			return err
		}); err != nil {
			t.Fatalf("ClaimCallbacksDue: %v", err)
		}
		if len(claimed) != 1 || claimed[0].ID != job.ID {
			t.Fatalf("claimed %d callbacks, want just the due one", len(claimed))
		}

		next := now.Add(time.Minute)
		if err := s.Jobs().ScheduleCallbackRetry(ctx, job.ID, next, "connection refused"); err != nil {
			t.Fatalf("ScheduleCallbackRetry: %v", err)
		}
		got, _ := s.Jobs().Get(ctx, job.ID)
		if got.Callback.Attempts != 1 {
			t.Errorf("callback attempts = %d, want 1", got.Callback.Attempts)
		}
		if got.Callback.LastError != "connection refused" {
			t.Errorf("last error = %q", got.Callback.LastError)
		}

		if err := s.Jobs().MarkCallbackDelivered(ctx, job.ID, now); err != nil {
			t.Fatalf("MarkCallbackDelivered: %v", err)
		}
		got, _ = s.Jobs().Get(ctx, job.ID)
		if got.Callback.DeliveredAt == nil {
			t.Error("DeliveredAt is nil after MarkCallbackDelivered")
		}
		if got.Callback.NextAttemptAt != nil {
			t.Error("a delivered callback is still scheduled for retry")
		}

		// Delivered callbacks are never claimed again.
		if err := s.WithTx(ctx, func(tx store.Store) error {
			var err error
			claimed, err = tx.Jobs().ClaimCallbacksDue(ctx, now.Add(time.Hour), 10)
			return err
		}); err != nil {
			t.Fatalf("ClaimCallbacksDue: %v", err)
		}
		if len(claimed) != 0 {
			t.Errorf("claimed %d delivered callbacks, want 0", len(claimed))
		}
	})

	// Giving up is not delivery: an abandoned callback must stop being claimed
	// while still reading as undelivered, or "which callbacks reached the
	// caller" becomes unanswerable.
	t.Run("abandon stops retries without claiming success", func(t *testing.T) {
		s := newStore(t)
		now := time.Now().UTC().Truncate(time.Millisecond)
		due := now.Add(-time.Minute)

		job := newJob(t, s, &store.Job{
			Callback: store.CallbackState{URL: "https://example.test/cb", NextAttemptAt: &due},
		})

		if err := s.Jobs().AbandonCallback(ctx, job.ID, "gave up after 10 attempts"); err != nil {
			t.Fatalf("AbandonCallback: %v", err)
		}

		got, _ := s.Jobs().Get(ctx, job.ID)
		if got.Callback.DeliveredAt != nil {
			t.Error("an abandoned callback was marked delivered")
		}
		if got.Callback.NextAttemptAt != nil {
			t.Error("an abandoned callback is still scheduled")
		}
		if got.Callback.LastError == "" {
			t.Error("the final failure was not kept for inspection")
		}

		var claimed []*store.Job
		if err := s.WithTx(ctx, func(tx store.Store) error {
			var err error
			claimed, err = tx.Jobs().ClaimCallbacksDue(ctx, now.Add(time.Hour), 10)
			return err
		}); err != nil {
			t.Fatalf("ClaimCallbacksDue: %v", err)
		}
		if len(claimed) != 0 {
			t.Errorf("claimed %d abandoned callbacks, want 0", len(claimed))
		}

		if err := s.Jobs().AbandonCallback(ctx, uuid.New(), "x"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("AbandonCallback on unknown job = %v, want ErrNotFound", err)
		}
	})

	t.Run("list is newest first and pages without gaps or repeats", func(t *testing.T) {
		s := newStore(t)
		newJob(t, s, &store.Job{Body: "first"})
		// A fanout writes several jobs in one transaction, so on Postgres they
		// share one now(): the cursor has to page through a timestamp tie.
		if err := s.WithTx(ctx, func(tx store.Store) error {
			for range 3 {
				newJob(t, tx, &store.Job{Body: "fanout"})
			}
			return nil
		}); err != nil {
			t.Fatalf("WithTx: %v", err)
		}
		newJob(t, s, &store.Job{Body: "last"})

		all, err := s.Jobs().List(ctx, store.JobFilter{})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(all) != 5 {
			t.Fatalf("List returned %d jobs, want 5", len(all))
		}
		if all[0].Body != "last" || all[4].Body != "first" {
			t.Errorf("order = %s..%s, want last..first", all[0].Body, all[4].Body)
		}
		for i := 1; i < len(all); i++ {
			if all[i].CreatedAt.After(all[i-1].CreatedAt) {
				t.Errorf("job %d is newer than job %d", i, i-1)
			}
		}

		var paged []*store.Job
		f := store.JobFilter{Limit: 2}
		for page := 0; ; page++ {
			if page > 5 {
				t.Fatal("paging did not terminate")
			}
			got, err := s.Jobs().List(ctx, f)
			if err != nil {
				t.Fatalf("List page %d: %v", page, err)
			}
			if len(got) == 0 {
				break
			}
			paged = append(paged, got...)
			f.After = store.CursorOf(got[len(got)-1])
		}
		if len(paged) != len(all) {
			t.Fatalf("paging returned %d jobs, want %d", len(paged), len(all))
		}
		for i := range all {
			if paged[i].ID != all[i].ID {
				t.Errorf("page position %d = %v, want %v", i, paged[i].ID, all[i].ID)
			}
		}
	})

	t.Run("list filters combine", func(t *testing.T) {
		s := newStore(t)
		dev, err := s.Devices().Create(ctx, &store.Device{Label: "d"}, "hash-filter")
		if err != nil {
			t.Fatalf("Create device: %v", err)
		}

		cm := newJob(t, s, &store.Job{Recipient: "+237670000001"})
		requested := newJob(t, s, &store.Job{Recipient: "+237670000002", RequestedDeviceID: &dev.ID})
		fr := newJob(t, s, &store.Job{Recipient: "+33600000001"})
		if err := s.Jobs().MarkAssigned(ctx, fr.ID, dev.ID, 0, time.Now()); err != nil {
			t.Fatalf("MarkAssigned: %v", err)
		}
		if err := s.Jobs().Complete(ctx, fr.ID, store.JobResult{
			Status: store.JobSent, PartsSent: 1, CompletedAt: time.Now(),
		}); err != nil {
			t.Fatalf("Complete: %v", err)
		}

		ids := func(t *testing.T, f store.JobFilter) []uuid.UUID {
			t.Helper()
			got, err := s.Jobs().List(ctx, f)
			if err != nil {
				t.Fatalf("List(%+v): %v", f, err)
			}
			out := make([]uuid.UUID, len(got))
			for i, j := range got {
				out[i] = j.ID
			}
			return out
		}
		want := func(t *testing.T, name string, got []uuid.UUID, want ...uuid.UUID) {
			t.Helper()
			if len(got) != len(want) {
				t.Errorf("%s: got %d jobs, want %d", name, len(got), len(want))
				return
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("%s: position %d = %v, want %v", name, i, got[i], want[i])
				}
			}
		}

		want(t, "status", ids(t, store.JobFilter{Statuses: []store.JobStatus{store.JobSent}}), fr.ID)
		want(t, "statuses", ids(t, store.JobFilter{
			Statuses: []store.JobStatus{store.JobSent, store.JobPending},
		}), fr.ID, requested.ID, cm.ID)
		// Requested and assigned both count as "this device's job".
		want(t, "device", ids(t, store.JobFilter{DeviceID: &dev.ID}), fr.ID, requested.ID)
		want(t, "prefix", ids(t, store.JobFilter{RecipientPrefix: "+237"}), requested.ID, cm.ID)
		want(t, "prefix and status", ids(t, store.JobFilter{
			RecipientPrefix: "+237", Statuses: []store.JobStatus{store.JobSent},
		}))
		// A LIKE wildcard in the input is a literal, not a pattern.
		want(t, "wildcard", ids(t, store.JobFilter{RecipientPrefix: "%"}))

		from, to := requested.CreatedAt, fr.CreatedAt
		want(t, "range is [from, to)", ids(t, store.JobFilter{CreatedFrom: &from, CreatedTo: &to}), requested.ID)
	})

	t.Run("dispatch counts per device and SIM over the windows", func(t *testing.T) {
		s := newStore(t)
		dev, err := s.Devices().Create(ctx, &store.Device{Label: "counted"}, "hash-count")
		if err != nil {
			t.Fatalf("Create device: %v", err)
		}
		now := time.Now().UTC()
		assign := func(sub int32, at time.Time) *store.Job {
			t.Helper()
			j, err := s.Jobs().Create(ctx, &store.Job{Recipient: "+22670000000", Body: "x", Mode: store.ModeQueued})
			if err != nil {
				t.Fatalf("Jobs.Create: %v", err)
			}
			if err := s.Jobs().MarkAssigned(ctx, j.ID, dev.ID, sub, at); err != nil {
				t.Fatalf("MarkAssigned: %v", err)
			}
			return j
		}

		assign(2, now.Add(-10*time.Minute))            // SIM 2, this hour
		assign(2, now.Add(-5*time.Hour))               // SIM 2, today
		assign(0, now.Add(-20*time.Minute))            // default SIM, this hour
		assign(2, now.Add(-30*time.Hour))              // outside the day
		released := assign(2, now.Add(-5*time.Minute)) // released: not counted
		failed := assign(2, now.Add(-15*time.Minute))  // failed: not counted
		sent := assign(2, now.Add(-25*time.Minute))    // sent: counted
		if err := s.Jobs().Release(ctx, released.ID, "x"); err != nil {
			t.Fatalf("Release: %v", err)
		}
		if err := s.Jobs().Complete(ctx, failed.ID, store.JobResult{Status: store.JobFailed, CompletedAt: now}); err != nil {
			t.Fatalf("Complete failed: %v", err)
		}
		if err := s.Jobs().Complete(ctx, sent.ID, store.JobResult{Status: store.JobSent, PartsSent: 1, CompletedAt: now}); err != nil {
			t.Fatalf("Complete sent: %v", err)
		}

		got, err := s.Jobs().CountDispatched(ctx, now.Add(-time.Hour), now.Add(-24*time.Hour))
		if err != nil {
			t.Fatalf("CountDispatched: %v", err)
		}
		var sim2, def *store.DispatchCount
		for i := range got {
			c := &got[i]
			if c.DeviceID != dev.ID {
				t.Errorf("count for unknown device %v", c.DeviceID)
				continue
			}
			if c.SubscriptionID == nil {
				def = c
			} else if *c.SubscriptionID == 2 {
				sim2 = c
			}
		}
		if sim2 == nil || sim2.LastHour != 2 || sim2.LastDay != 3 {
			t.Errorf("SIM 2 = %+v, want 2 this hour, 3 today", sim2)
		}
		if def == nil || def.LastHour != 1 || def.LastDay != 1 {
			t.Errorf("default SIM = %+v, want 1 and 1", def)
		}

		// A released job forgets the SIM it was handed to.
		j, err := s.Jobs().Get(ctx, released.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if j.AssignedSubscriptionID != nil {
			t.Errorf("released job still names SIM %d", *j.AssignedSubscriptionID)
		}
		j, _ = s.Jobs().Get(ctx, sent.ID)
		if j.AssignedSubscriptionID == nil || *j.AssignedSubscriptionID != 2 {
			t.Errorf("sent job SIM = %v, want 2", j.AssignedSubscriptionID)
		}
	})

	t.Run("count by bucket aligns on from and groups by status", func(t *testing.T) {
		s := newStore(t)
		for range 3 {
			if _, err := s.Jobs().Create(ctx, &store.Job{Recipient: "+22670000000", Body: "x", Mode: store.ModeQueued}); err != nil {
				t.Fatal(err)
			}
		}
		j, err := s.Jobs().Create(ctx, &store.Job{Recipient: "+22670000000", Body: "y", Mode: store.ModeQueued})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Jobs().Cancel(ctx, j.ID, "x"); err != nil {
			t.Fatal(err)
		}

		from := time.Now().Add(-time.Hour).Truncate(time.Minute)
		got, err := s.Jobs().CountByBucket(ctx, store.AllAccounts(), from, 10*time.Minute)
		if err != nil {
			t.Fatalf("CountByBucket: %v", err)
		}
		byStatus := map[store.JobStatus]int{}
		for _, c := range got {
			off := c.Start.Sub(from)
			if off < 0 || off%(10*time.Minute) != 0 {
				t.Errorf("bucket start %v not aligned on %v", c.Start, from)
			}
			byStatus[c.Status] += c.Count
		}
		if byStatus[store.JobPending] != 3 || byStatus[store.JobCancelled] != 1 {
			t.Errorf("counts = %v, want 3 pending and 1 cancelled", byStatus)
		}

		later, err := s.Jobs().CountByBucket(ctx, store.AllAccounts(), time.Now().Add(time.Hour), time.Minute)
		if err != nil || len(later) != 0 {
			t.Errorf("window in the future: %v, %v; want nothing", later, err)
		}
	})

	t.Run("count by status", func(t *testing.T) {
		s := newStore(t)
		before := newJob(t, s, &store.Job{})
		newJob(t, s, &store.Job{})
		cancelled := newJob(t, s, &store.Job{})
		if err := s.Jobs().Cancel(ctx, cancelled.ID, "test"); err != nil {
			t.Fatalf("Cancel: %v", err)
		}

		all, err := s.Jobs().CountByStatus(ctx, store.AllAccounts(), time.Time{})
		if err != nil {
			t.Fatalf("CountByStatus: %v", err)
		}
		if all[store.JobPending] != 3-1 || all[store.JobCancelled] != 1 {
			t.Errorf("counts = %v, want pending=2 cancelled=1", all)
		}
		if _, ok := all[store.JobSent]; ok {
			t.Error("a status with no jobs must be absent, not zero")
		}

		// since is inclusive and excludes what came before it.
		recent, err := s.Jobs().CountByStatus(ctx, store.AllAccounts(), before.CreatedAt.Add(time.Microsecond))
		if err != nil {
			t.Fatalf("CountByStatus: %v", err)
		}
		if recent[store.JobPending]+recent[store.JobCancelled] != 2 {
			t.Errorf("counts since the second job = %v, want 2 jobs", recent)
		}
	})
}

func testEnrollment(t *testing.T, newStore Factory) {
	ctx := context.Background()

	t.Run("create and consume", func(t *testing.T) {
		s := newStore(t)
		now := time.Now().UTC().Truncate(time.Millisecond)

		tok, err := s.Enrollments().Create(ctx, uuid.Nil, "hash-e1", now.Add(time.Hour))
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if tok.ConsumedAt != nil {
			t.Error("a new token is already consumed")
		}

		dev, err := s.Devices().Create(ctx, &store.Device{Label: "d"}, "hash-d1")
		if err != nil {
			t.Fatalf("Create device: %v", err)
		}

		consumed, err := s.Enrollments().Consume(ctx, "hash-e1", dev.ID, now)
		if err != nil {
			t.Fatalf("Consume: %v", err)
		}
		if consumed.ConsumedAt == nil {
			t.Error("ConsumedAt not set")
		}
		if consumed.DeviceID == nil || *consumed.DeviceID != dev.ID {
			t.Errorf("DeviceID = %v, want %v", consumed.DeviceID, dev.ID)
		}
	})

	t.Run("second use is ErrConflict", func(t *testing.T) {
		s := newStore(t)
		now := time.Now().UTC()
		if _, err := s.Enrollments().Create(ctx, uuid.Nil, "hash-e2", now.Add(time.Hour)); err != nil {
			t.Fatalf("Create: %v", err)
		}
		d1, _ := s.Devices().Create(ctx, &store.Device{Label: "a"}, "hash-a")
		d2, _ := s.Devices().Create(ctx, &store.Device{Label: "b"}, "hash-b")

		if _, err := s.Enrollments().Consume(ctx, "hash-e2", d1.ID, now); err != nil {
			t.Fatalf("first Consume: %v", err)
		}
		if _, err := s.Enrollments().Consume(ctx, "hash-e2", d2.ID, now); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("second Consume = %v, want ErrConflict", err)
		}
	})

	t.Run("expired is ErrTokenExpired, not conflict", func(t *testing.T) {
		s := newStore(t)
		now := time.Now().UTC()
		if _, err := s.Enrollments().Create(ctx, uuid.Nil, "hash-e3", now.Add(-time.Minute)); err != nil {
			t.Fatalf("Create: %v", err)
		}
		dev, _ := s.Devices().Create(ctx, &store.Device{Label: "d"}, "hash-d3")

		_, err := s.Enrollments().Consume(ctx, "hash-e3", dev.ID, now)
		if !errors.Is(err, store.ErrTokenExpired) {
			t.Fatalf("Consume = %v, want ErrTokenExpired", err)
		}
		// The two are distinguished because the operator's fix differs.
		if errors.Is(err, store.ErrConflict) {
			t.Error("expiry was reported as a conflict")
		}
	})

	t.Run("unknown token is ErrNotFound", func(t *testing.T) {
		s := newStore(t)
		_, err := s.Enrollments().Consume(ctx, "never-created", uuid.New(), time.Now())
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("Consume = %v, want ErrNotFound", err)
		}
	})

	t.Run("delete expired keeps consumed ones", func(t *testing.T) {
		s := newStore(t)
		now := time.Now().UTC()

		if _, err := s.Enrollments().Create(ctx, uuid.Nil, "expired", now.Add(-time.Hour)); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if _, err := s.Enrollments().Create(ctx, uuid.Nil, "live", now.Add(time.Hour)); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if _, err := s.Enrollments().Create(ctx, uuid.Nil, "used", now.Add(-time.Hour)); err != nil {
			t.Fatalf("Create: %v", err)
		}
		dev, _ := s.Devices().Create(ctx, &store.Device{Label: "d"}, "hash-d4")
		// Consume before it expired.
		if _, err := s.Enrollments().Consume(ctx, "used", dev.ID, now.Add(-2*time.Hour)); err != nil {
			t.Fatalf("Consume: %v", err)
		}

		n, err := s.Enrollments().DeleteExpired(ctx, now)
		if err != nil {
			t.Fatalf("DeleteExpired: %v", err)
		}
		if n != 1 {
			t.Errorf("deleted %d tokens, want 1 (only the unredeemed expired one)", n)
		}
	})
}

func testEvents(t *testing.T, newStore Factory) {
	ctx := context.Background()

	t.Run("append and list in order", func(t *testing.T) {
		s := newStore(t)
		job, err := s.Jobs().Create(ctx, &store.Job{
			Recipient: "+1", Body: "x", Mode: store.ModeQueued,
		})
		if err != nil {
			t.Fatalf("Create job: %v", err)
		}
		dev, _ := s.Devices().Create(ctx, &store.Device{Label: "d"}, "hash-ev")

		base := time.Now().UTC().Truncate(time.Millisecond)
		for i, status := range []store.JobStatus{store.JobPending, store.JobAssigned, store.JobSent} {
			err := s.Events().Append(ctx, &store.JobEvent{
				JobID:     job.ID,
				Status:    status,
				DeviceID:  &dev.ID,
				Reason:    string(status),
				CreatedAt: base.Add(time.Duration(i) * time.Second),
			})
			if err != nil {
				t.Fatalf("Append: %v", err)
			}
		}

		got, err := s.Events().ListByJob(ctx, job.ID)
		if err != nil {
			t.Fatalf("ListByJob: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("listed %d events, want 3", len(got))
		}
		if got[0].Status != store.JobPending || got[2].Status != store.JobSent {
			t.Errorf("events out of order: %q then %q", got[0].Status, got[2].Status)
		}
		if got[0].DeviceID == nil || *got[0].DeviceID != dev.ID {
			t.Errorf("device id lost: %v", got[0].DeviceID)
		}
	})

	t.Run("event without a status is allowed", func(t *testing.T) {
		s := newStore(t)
		job, err := s.Jobs().Create(ctx, &store.Job{
			Recipient: "+1", Body: "x", Mode: store.ModeQueued,
		})
		if err != nil {
			t.Fatalf("Create job: %v", err)
		}
		// Not every event is a transition: a retry or a callback attempt has no
		// status of its own.
		if err := s.Events().Append(ctx, &store.JobEvent{
			JobID: job.ID, Reason: "callback attempt 3",
		}); err != nil {
			t.Fatalf("Append: %v", err)
		}
		got, err := s.Events().ListByJob(ctx, job.ID)
		if err != nil {
			t.Fatalf("ListByJob: %v", err)
		}
		if len(got) != 1 || got[0].Status != "" {
			t.Errorf("got %d events, first status %q; want 1 with no status", len(got), got[0].Status)
		}
	})

	t.Run("list for unknown job is empty, not an error", func(t *testing.T) {
		s := newStore(t)
		got, err := s.Events().ListByJob(ctx, uuid.New())
		if err != nil {
			t.Fatalf("ListByJob: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %d events, want 0", len(got))
		}
	})
}

func testTransactions(t *testing.T, newStore Factory) {
	ctx := context.Background()

	t.Run("commit persists", func(t *testing.T) {
		s := newStore(t)
		var id uuid.UUID
		err := s.WithTx(ctx, func(tx store.Store) error {
			dev, err := tx.Devices().Create(ctx, &store.Device{Label: "committed"}, "hash-tx1")
			if err != nil {
				return err
			}
			id = dev.ID
			return nil
		})
		if err != nil {
			t.Fatalf("WithTx: %v", err)
		}
		if _, err := s.Devices().Get(ctx, id); err != nil {
			t.Fatalf("committed device is missing: %v", err)
		}
	})

	// The property enrollment depends on: consuming a token and creating a
	// device succeed or fail together.
	t.Run("error rolls everything back", func(t *testing.T) {
		s := newStore(t)
		sentinel := errors.New("deliberate")

		err := s.WithTx(ctx, func(tx store.Store) error {
			if _, err := tx.Devices().Create(ctx, &store.Device{Label: "doomed"}, "hash-tx2"); err != nil {
				return err
			}
			if _, err := tx.Jobs().Create(ctx, &store.Job{
				Recipient: "+1", Body: "doomed", Mode: store.ModeQueued,
			}); err != nil {
				return err
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("WithTx = %v, want the sentinel", err)
		}

		if _, err := s.Devices().GetByTokenHash(ctx, "hash-tx2"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("rolled-back device survived: %v", err)
		}
		devices, err := s.Devices().List(ctx, store.AllAccounts())
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(devices) != 0 {
			t.Errorf("%d devices survived a rollback, want 0", len(devices))
		}
	})

	t.Run("nested WithTx is refused", func(t *testing.T) {
		s := newStore(t)
		err := s.WithTx(ctx, func(tx store.Store) error {
			return tx.WithTx(ctx, func(store.Store) error { return nil })
		})
		if err == nil {
			t.Fatal("nested WithTx was allowed")
		}
	})
}

func testAccounts(t *testing.T, newStore Factory) {
	ctx := context.Background()

	t.Run("default account exists and leads the list", func(t *testing.T) {
		s := newStore(t)
		acc, err := s.Accounts().Get(ctx, store.DefaultAccountID)
		if err != nil {
			t.Fatalf("Get default: %v", err)
		}
		if acc.Name != "default" {
			t.Errorf("default account name = %q", acc.Name)
		}
		b, err := s.Accounts().Create(ctx, "acme")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		all, err := s.Accounts().List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(all) != 2 || all[0].ID != store.DefaultAccountID || all[1].ID != b.ID {
			t.Errorf("List = %+v, want default then acme", all)
		}
		if _, err := s.Accounts().Get(ctx, uuid.New()); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Get unknown = %v, want ErrNotFound", err)
		}
	})

	t.Run("rows without an account land in the default one", func(t *testing.T) {
		s := newStore(t)
		d, err := s.Devices().Create(ctx, &store.Device{Label: "old caller"}, "tok-default")
		if err != nil {
			t.Fatalf("Create device: %v", err)
		}
		j, err := s.Jobs().Create(ctx, &store.Job{Recipient: "+22670000000", Body: "x", Mode: store.ModeQueued})
		if err != nil {
			t.Fatalf("Create job: %v", err)
		}
		tok, err := s.Enrollments().Create(ctx, uuid.Nil, "enr-default", time.Now().Add(time.Hour))
		if err != nil {
			t.Fatalf("Create token: %v", err)
		}
		if d.AccountID != store.DefaultAccountID || j.AccountID != store.DefaultAccountID || tok.AccountID != store.DefaultAccountID {
			t.Errorf("accounts = %v / %v / %v, want the default account for all three", d.AccountID, j.AccountID, tok.AccountID)
		}
	})

	t.Run("one account never sees another's devices or jobs", func(t *testing.T) {
		s := newStore(t)
		a, err := s.Accounts().Create(ctx, "a")
		if err != nil {
			t.Fatal(err)
		}
		b, err := s.Accounts().Create(ctx, "b")
		if err != nil {
			t.Fatal(err)
		}
		da, err := s.Devices().Create(ctx, &store.Device{Label: "a-phone", AccountID: a.ID}, "tok-a")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Devices().Create(ctx, &store.Device{Label: "b-phone", AccountID: b.ID}, "tok-b"); err != nil {
			t.Fatal(err)
		}
		for _, acc := range []uuid.UUID{a.ID, a.ID, b.ID} {
			if _, err := s.Jobs().Create(ctx, &store.Job{Recipient: "+22670000000", Body: "x", Mode: store.ModeQueued, AccountID: acc}); err != nil {
				t.Fatal(err)
			}
		}

		devs, err := s.Devices().List(ctx, store.OnlyAccount(a.ID))
		if err != nil {
			t.Fatal(err)
		}
		if len(devs) != 1 || devs[0].ID != da.ID || devs[0].AccountID != a.ID {
			t.Errorf("a's devices = %+v, want only a-phone", devs)
		}
		all, _ := s.Devices().List(ctx, store.AllAccounts())
		if len(all) != 2 {
			t.Errorf("all devices = %d, want 2", len(all))
		}

		jobs, err := s.Jobs().List(ctx, store.JobFilter{AccountID: &b.ID})
		if err != nil {
			t.Fatal(err)
		}
		if len(jobs) != 1 || jobs[0].AccountID != b.ID {
			t.Errorf("b's jobs = %d, want 1 of b's", len(jobs))
		}

		counts, err := s.Jobs().CountByStatus(ctx, store.OnlyAccount(a.ID), time.Now().Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if counts[store.JobPending] != 2 {
			t.Errorf("a's pending count = %d, want 2", counts[store.JobPending])
		}
		buckets, err := s.Jobs().CountByBucket(ctx, store.OnlyAccount(b.ID), time.Now().Add(-time.Hour).Truncate(time.Minute), 10*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, c := range buckets {
			n += c.Count
		}
		if n != 1 {
			t.Errorf("b's activity = %d jobs, want 1", n)
		}
	})

	t.Run("an enrollment token carries its account, and Lookup reads it without consuming", func(t *testing.T) {
		s := newStore(t)
		a, err := s.Accounts().Create(ctx, "a")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Enrollments().Create(ctx, a.ID, "enr-a", time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		got, err := s.Enrollments().Lookup(ctx, "enr-a")
		if err != nil {
			t.Fatalf("Lookup: %v", err)
		}
		if got.AccountID != a.ID || got.ConsumedAt != nil {
			t.Errorf("Lookup = %+v, want a's unconsumed token", got)
		}
		if _, err := s.Enrollments().Lookup(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Lookup unknown = %v, want ErrNotFound", err)
		}
	})

	t.Run("rekey moves the device to the enrolling account", func(t *testing.T) {
		s := newStore(t)
		b, err := s.Accounts().Create(ctx, "b")
		if err != nil {
			t.Fatal(err)
		}
		d, err := s.Devices().Create(ctx, &store.Device{Label: "p", HardwareHash: "hw-move"}, "tok-1")
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.Devices().Rekey(ctx, d.ID, &store.Device{Label: "p", AccountID: b.ID}, "tok-2")
		if err != nil {
			t.Fatalf("Rekey: %v", err)
		}
		if got.AccountID != b.ID {
			t.Errorf("account after rekey = %v, want b", got.AccountID)
		}
	})
}

func testUSSD(t *testing.T, newStore Factory) {
	ctx := context.Background()

	ussd := func(t *testing.T, s store.Store, sensitive bool) *store.Job {
		t.Helper()
		j, err := s.Jobs().Create(ctx, &store.Job{
			Kind: store.KindUSSD, Body: "*144*1234#", Mode: store.ModeQueued,
			Replies: []string{"2", "1"}, Sensitive: sensitive,
			Paging: &policy.Paging{Marker: "Suivant", Reply: "n", MaxPages: 4}, Extract: `(?P<solde>[0-9.]+) FCFA`,
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		return j
	}

	t.Run("kind, replies and sensitive round-trip; the default kind is sms", func(t *testing.T) {
		s := newStore(t)
		j := ussd(t, s, true)
		got, err := s.Jobs().Get(ctx, j.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Kind != store.KindUSSD || got.Body != "*144*1234#" || !got.Sensitive {
			t.Errorf("round trip lost fields: %+v", got)
		}
		if len(got.Replies) != 2 || got.Replies[0] != "2" || got.Replies[1] != "1" {
			t.Errorf("Replies = %q, want [2 1]", got.Replies)
		}
		if len(got.Responses) != 0 {
			t.Errorf("Responses = %q before any result", got.Responses)
		}
		if got.Paging == nil || got.Paging.Marker != "Suivant" || got.Paging.MaxPages != 4 || got.Extract == "" {
			t.Errorf("paging %+v, extract %q did not round-trip", got.Paging, got.Extract)
		}
		if plain, _ := s.Jobs().Create(ctx, &store.Job{Body: "hi", Recipient: "+22670000000", Mode: store.ModeQueued}); plain != nil && plain.Paging != nil {
			t.Errorf("a job without paging read back %+v", plain.Paging)
		}

		sms, err := s.Jobs().Create(ctx, &store.Job{Recipient: "+22670000000", Body: "x", Mode: store.ModeQueued})
		if err != nil {
			t.Fatalf("Create sms: %v", err)
		}
		if sms.Kind != store.KindSMS {
			t.Errorf("Kind = %q, want sms by default", sms.Kind)
		}
	})

	t.Run("complete keeps responses and wipes a sensitive job's secret", func(t *testing.T) {
		s := newStore(t)
		secret, plain := ussd(t, s, true), ussd(t, s, false)
		for _, j := range []*store.Job{secret, plain} {
			err := s.Jobs().Complete(ctx, j.ID, store.JobResult{
				Status: store.JobSent, CompletedAt: time.Now(), Responses: []string{"Menu", "Solde: 5000 FCFA"},
			})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
		}

		got, _ := s.Jobs().Get(ctx, secret.ID)
		if got.Body != "" || len(got.Replies) != 0 {
			t.Errorf("sensitive job kept body %q, replies %q", got.Body, got.Replies)
		}
		if len(got.Responses) != 2 || got.Responses[1] != "Solde: 5000 FCFA" {
			t.Errorf("Responses = %q", got.Responses)
		}
		kept, _ := s.Jobs().Get(ctx, plain.ID)
		if kept.Body != "*144*1234#" || len(kept.Replies) != 2 {
			t.Errorf("a job not marked sensitive lost its code: %+v", kept)
		}

		// A repeat without responses leaves them alone.
		if err := s.Jobs().Complete(ctx, plain.ID, store.JobResult{Status: store.JobSent, CompletedAt: time.Now()}); err != nil {
			t.Fatalf("repeat Complete: %v", err)
		}
		if again, _ := s.Jobs().Get(ctx, plain.ID); len(again.Responses) != 2 {
			t.Errorf("a repeated result wiped the responses: %q", again.Responses)
		}
	})

	t.Run("cancel wipes a sensitive job's secret", func(t *testing.T) {
		s := newStore(t)
		j := ussd(t, s, true)
		if err := s.Jobs().Cancel(ctx, j.ID, "changed my mind"); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
		if got, _ := s.Jobs().Get(ctx, j.ID); got.Body != "" || len(got.Replies) != 0 {
			t.Errorf("cancelled sensitive job kept body %q, replies %q", got.Body, got.Replies)
		}
	})

	t.Run("ussd jobs are listed by kind and left out of message counts", func(t *testing.T) {
		s := newStore(t)
		dev, err := s.Devices().Create(ctx, &store.Device{Label: "d"}, "hash-ussd")
		if err != nil {
			t.Fatalf("Create device: %v", err)
		}
		now := time.Now().UTC()
		u := ussd(t, s, false)
		if err := s.Jobs().MarkAssigned(ctx, u.ID, dev.ID, 0, now); err != nil {
			t.Fatalf("MarkAssigned: %v", err)
		}
		m, err := s.Jobs().Create(ctx, &store.Job{Recipient: "+22670000000", Body: "x", Mode: store.ModeQueued})
		if err != nil {
			t.Fatalf("Create sms: %v", err)
		}
		if err := s.Jobs().MarkAssigned(ctx, m.ID, dev.ID, 0, now); err != nil {
			t.Fatalf("MarkAssigned: %v", err)
		}

		counts, err := s.Jobs().CountDispatched(ctx, now.Add(-time.Hour), now.Add(-24*time.Hour))
		if err != nil {
			t.Fatalf("CountDispatched: %v", err)
		}
		if len(counts) != 1 || counts[0].LastHour != 1 {
			t.Errorf("CountDispatched = %+v, want the SMS alone", counts)
		}
		byStatus, err := s.Jobs().CountByStatus(ctx, store.AllAccounts(), now.Add(-time.Hour))
		if err != nil {
			t.Fatalf("CountByStatus: %v", err)
		}
		if byStatus[store.JobAssigned] != 1 {
			t.Errorf("CountByStatus = %v, want one assigned SMS", byStatus)
		}
		buckets, err := s.Jobs().CountByBucket(ctx, store.AllAccounts(), now.Add(-time.Hour), time.Hour)
		if err != nil {
			t.Fatalf("CountByBucket: %v", err)
		}
		total := 0
		for _, b := range buckets {
			total += b.Count
		}
		if total != 1 {
			t.Errorf("CountByBucket counted %d jobs, want the SMS alone", total)
		}

		listed, err := s.Jobs().List(ctx, store.JobFilter{Kinds: []store.JobKind{store.KindUSSD}})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(listed) != 1 || listed[0].ID != u.ID {
			t.Errorf("List(kind=ussd) = %d jobs, want the USSD one", len(listed))
		}
	})

	t.Run("device capabilities round-trip through create, update and rekey", func(t *testing.T) {
		s := newStore(t)
		dev, err := s.Devices().Create(ctx, &store.Device{Label: "d", Capabilities: []string{store.CapUSSD}}, "hash-cap")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if !dev.Can(store.CapUSSD) || dev.Can(store.CapInboundSMS) {
			t.Errorf("Capabilities after create = %q", dev.Capabilities)
		}
		if err := s.Devices().UpdateInfo(ctx, dev.ID, &store.Device{Label: "d", Capabilities: []string{store.CapUSSD, store.CapInboundSMS}}); err != nil {
			t.Fatalf("UpdateInfo: %v", err)
		}
		if got, _ := s.Devices().Get(ctx, dev.ID); !got.Can(store.CapInboundSMS) {
			t.Errorf("Capabilities after update = %q", got.Capabilities)
		}
		rekeyed, err := s.Devices().Rekey(ctx, dev.ID, &store.Device{Label: "d"}, "hash-cap-2")
		if err != nil {
			t.Fatalf("Rekey: %v", err)
		}
		if len(rekeyed.Capabilities) != 0 {
			t.Errorf("an agent reporting none kept %q", rekeyed.Capabilities)
		}
	})
}
