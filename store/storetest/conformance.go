package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

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

	t.Run("Devices", func(t *testing.T) { testDevices(t, newStore) })
	t.Run("Jobs", func(t *testing.T) { testJobs(t, newStore) })
	t.Run("Enrollment", func(t *testing.T) { testEnrollment(t, newStore) })
	t.Run("Events", func(t *testing.T) { testEvents(t, newStore) })
	t.Run("Transactions", func(t *testing.T) { testTransactions(t, newStore) })
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
		if got.LastSeenAt == nil {
			t.Error("LastSeenAt is nil after Touch")
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
		if err := s.Devices().UpdateInfo(ctx, dev.ID, &store.Device{
			Label: "new", PhoneNumber: "+1", Carrier: "Orange",
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
		if got.Enabled {
			t.Error("SetEnabled(false) did not take")
		}
	})

	t.Run("list returns everything", func(t *testing.T) {
		s := newStore(t)
		for i, hash := range []string{"h1", "h2", "h3"} {
			if _, err := s.Devices().Create(ctx, &store.Device{Label: string(rune('a' + i))}, hash); err != nil {
				t.Fatalf("Create: %v", err)
			}
		}
		got, err := s.Devices().List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 3 {
			t.Errorf("List returned %d devices, want 3", len(got))
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
		if err := s.Jobs().MarkAssigned(ctx, job.ID, dev.ID, at); err != nil {
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
		if err := s.Jobs().MarkAssigned(ctx, job.ID, dev.ID, at); !errors.Is(err, store.ErrConflict) {
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
		err := s.Jobs().MarkAssigned(ctx, uuid.New(), uuid.New(), time.Now())
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

		if err := s.Jobs().MarkAssigned(ctx, mine.ID, dev.ID, time.Now()); err != nil {
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
}

func testEnrollment(t *testing.T, newStore Factory) {
	ctx := context.Background()

	t.Run("create and consume", func(t *testing.T) {
		s := newStore(t)
		now := time.Now().UTC().Truncate(time.Millisecond)

		tok, err := s.Enrollments().Create(ctx, "hash-e1", now.Add(time.Hour))
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
		if _, err := s.Enrollments().Create(ctx, "hash-e2", now.Add(time.Hour)); err != nil {
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
		if _, err := s.Enrollments().Create(ctx, "hash-e3", now.Add(-time.Minute)); err != nil {
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

		if _, err := s.Enrollments().Create(ctx, "expired", now.Add(-time.Hour)); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if _, err := s.Enrollments().Create(ctx, "live", now.Add(time.Hour)); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if _, err := s.Enrollments().Create(ctx, "used", now.Add(-time.Hour)); err != nil {
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
		devices, err := s.Devices().List(ctx)
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
