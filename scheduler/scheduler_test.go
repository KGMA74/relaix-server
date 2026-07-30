package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	v1 "github.com/KGMA74/relaix-server/gen/smsgateway/v1"
	"github.com/KGMA74/relaix-server/hub"
	"github.com/KGMA74/relaix-server/store"
	"github.com/KGMA74/relaix-server/store/storetest"
)

// fakeHub records what the scheduler pushed and can be told to reject sends.
type fakeHub struct {
	mu       sync.Mutex
	ready    []hub.DeviceState
	sent     map[uuid.UUID][]*v1.ServerMessage
	sendErr  map[uuid.UUID]error
	listErr  error
	sendCall int
}

func newFakeHub(deviceIDs ...uuid.UUID) *fakeHub {
	h := &fakeHub{
		sent:    make(map[uuid.UUID][]*v1.ServerMessage),
		sendErr: make(map[uuid.UUID]error),
	}
	for _, id := range deviceIDs {
		h.ready = append(h.ready, hub.DeviceState{DeviceID: id})
	}
	return h
}

func (h *fakeHub) ListReady(context.Context) ([]hub.DeviceState, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.listErr != nil {
		return nil, h.listErr
	}
	return append([]hub.DeviceState(nil), h.ready...), nil
}

func (h *fakeHub) SendJob(_ context.Context, deviceID uuid.UUID, msg *v1.ServerMessage) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sendCall++
	if err := h.sendErr[deviceID]; err != nil {
		return err
	}
	h.sent[deviceID] = append(h.sent[deviceID], msg)
	return nil
}

func (h *fakeHub) sentTo(deviceID uuid.UUID) []*v1.ServerMessage {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*v1.ServerMessage(nil), h.sent[deviceID]...)
}

func (h *fakeHub) totalSent() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, msgs := range h.sent {
		n += len(msgs)
	}
	return n
}

var testNow = time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

func newScheduler(t *testing.T, st store.Store, h Hub, opts Options) *Scheduler {
	t.Helper()
	if opts.Now == nil {
		opts.Now = func() time.Time { return testNow }
	}
	opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(st, h, opts)
}

func ptr[T any](v T) *T { return &v }

func TestTickAssignsPendingJob(t *testing.T) {
	st := storetest.New()
	dev := uuid.New()
	h := newFakeHub(dev)
	s := newScheduler(t, st, h, Options{})

	job := st.SeedJob(&store.Job{Recipient: "+33600000000", Body: "hi", Mode: store.ModeQueued})

	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got := st.JobByID(job.ID)
	if got.Status != store.JobAssigned {
		t.Errorf("status = %q, want %q", got.Status, store.JobAssigned)
	}
	if got.AssignedDeviceID == nil || *got.AssignedDeviceID != dev {
		t.Errorf("assigned device = %v, want %v", got.AssignedDeviceID, dev)
	}
	if got.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", got.Attempts)
	}

	msgs := h.sentTo(dev)
	if len(msgs) != 1 {
		t.Fatalf("sent %d messages, want 1", len(msgs))
	}
	if id := msgs[0].GetSendSmsJob().GetJobId(); id != job.ID.String() {
		t.Errorf("pushed job id = %q, want %q", id, job.ID)
	}
	if body := msgs[0].GetSendSmsJob().GetBody(); body != "hi" {
		t.Errorf("pushed body = %q, want %q", body, "hi")
	}
}

func TestTickHonoursPriority(t *testing.T) {
	st := storetest.New()
	dev := uuid.New()
	h := newFakeHub(dev)
	// One job per tick, so the order the scheduler picks is observable.
	s := newScheduler(t, st, h, Options{BatchSize: 1})

	low := st.SeedJob(&store.Job{Recipient: "+1", Body: "low", Mode: store.ModeQueued, Priority: 1, CreatedAt: testNow.Add(-time.Hour)})
	high := st.SeedJob(&store.Job{Recipient: "+2", Body: "high", Mode: store.ModeQueued, Priority: 9, CreatedAt: testNow})

	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if st.JobByID(high.ID).Status != store.JobAssigned {
		t.Error("high priority job was not assigned first")
	}
	if st.JobByID(low.ID).Status != store.JobPending {
		t.Error("low priority job should still be pending")
	}
}

func TestExplicitDeviceIsUsed(t *testing.T) {
	st := storetest.New()
	wanted, other := uuid.New(), uuid.New()
	h := newFakeHub(other, wanted)
	s := newScheduler(t, st, h, Options{})

	job := st.SeedJob(&store.Job{
		Recipient:         "+33600000000",
		Body:              "hi",
		Mode:              store.ModeQueued,
		RequestedDeviceID: &wanted,
	})

	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got := st.JobByID(job.ID)
	if got.AssignedDeviceID == nil || *got.AssignedDeviceID != wanted {
		t.Fatalf("assigned to %v, want the requested device %v", got.AssignedDeviceID, wanted)
	}
	if len(h.sentTo(other)) != 0 {
		t.Error("job was pushed to a device the caller did not ask for")
	}
}

// A caller naming a device means a specific SIM or number; substituting another
// would be wrong in a way the caller cannot detect.
func TestExplicitDeviceIsNeverSubstituted(t *testing.T) {
	st := storetest.New()
	absent, available := uuid.New(), uuid.New()
	h := newFakeHub(available)
	s := newScheduler(t, st, h, Options{})

	job := st.SeedJob(&store.Job{
		Recipient:         "+33600000000",
		Body:              "hi",
		Mode:              store.ModeQueued,
		RequestedDeviceID: &absent,
	})

	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if got := st.JobByID(job.ID); got.Status != store.JobPending {
		t.Errorf("status = %q, want %q (job must wait, not be rerouted)", got.Status, store.JobPending)
	}
	if h.totalSent() != 0 {
		t.Error("job was rerouted to another device")
	}
}

func TestImmediateFailsFastWhenNoDeviceReady(t *testing.T) {
	st := storetest.New()
	h := newFakeHub() // empty fleet
	s := newScheduler(t, st, h, Options{})

	job := st.SeedJob(&store.Job{Recipient: "+1", Body: "otp", Mode: store.ModeImmediate})

	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got := st.JobByID(job.ID)
	if got.Status != store.JobFailed {
		t.Fatalf("status = %q, want %q", got.Status, store.JobFailed)
	}
	if got.ErrorCode != "no_device_available" {
		t.Errorf("error code = %q, want %q", got.ErrorCode, "no_device_available")
	}
}

func TestImmediateFailsFastWhenRequestedDeviceNotReady(t *testing.T) {
	st := storetest.New()
	absent := uuid.New()
	h := newFakeHub(uuid.New())
	s := newScheduler(t, st, h, Options{})

	job := st.SeedJob(&store.Job{
		Recipient:         "+1",
		Body:              "otp",
		Mode:              store.ModeImmediate,
		RequestedDeviceID: &absent,
	})

	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got := st.JobByID(job.ID)
	if got.Status != store.JobFailed {
		t.Fatalf("status = %q, want %q", got.Status, store.JobFailed)
	}
	if got.ErrorCode != "device_unavailable" {
		t.Errorf("error code = %q, want %q", got.ErrorCode, "device_unavailable")
	}
}

func TestQueuedWaitsThenSendsWhenADeviceAppears(t *testing.T) {
	st := storetest.New()
	h := newFakeHub() // nobody ready yet
	s := newScheduler(t, st, h, Options{})

	job := st.SeedJob(&store.Job{Recipient: "+1", Body: "bulk", Mode: store.ModeQueued})

	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("first Tick: %v", err)
	}
	if got := st.JobByID(job.ID); got.Status != store.JobPending {
		t.Fatalf("status = %q, want %q", got.Status, store.JobPending)
	}

	// A phone comes online.
	dev := uuid.New()
	h.mu.Lock()
	h.ready = append(h.ready, hub.DeviceState{DeviceID: dev})
	h.mu.Unlock()

	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("second Tick: %v", err)
	}
	if got := st.JobByID(job.ID); got.Status != store.JobAssigned {
		t.Fatalf("status = %q, want %q", got.Status, store.JobAssigned)
	}
}

func TestScheduledJobIsHeldBackUntilDue(t *testing.T) {
	st := storetest.New()
	dev := uuid.New()
	h := newFakeHub(dev)

	now := testNow
	s := newScheduler(t, st, h, Options{Now: func() time.Time { return now }})

	job := st.SeedJob(&store.Job{
		Recipient:   "+1",
		Body:        "later",
		Mode:        store.ModeQueued,
		ScheduledAt: ptr(testNow.Add(time.Hour)),
	})

	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("Tick before due: %v", err)
	}
	if got := st.JobByID(job.ID); got.Status != store.JobPending {
		t.Fatalf("scheduled job was sent early: status = %q", got.Status)
	}

	now = testNow.Add(2 * time.Hour)
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("Tick after due: %v", err)
	}
	if got := st.JobByID(job.ID); got.Status != store.JobAssigned {
		t.Fatalf("scheduled job was not sent once due: status = %q", got.Status)
	}
}

// Late delivery of a time-sensitive message is worse than none, so expiry beats
// device availability.
func TestExpiredJobIsFailedNotSent(t *testing.T) {
	st := storetest.New()
	dev := uuid.New()
	h := newFakeHub(dev)
	s := newScheduler(t, st, h, Options{})

	job := st.SeedJob(&store.Job{
		Recipient: "+1",
		Body:      "stale otp",
		Mode:      store.ModeQueued,
		ExpiresAt: ptr(testNow.Add(-time.Minute)),
	})

	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got := st.JobByID(job.ID)
	if got.Status != store.JobFailed {
		t.Fatalf("status = %q, want %q", got.Status, store.JobFailed)
	}
	if got.ErrorCode != "expired" {
		t.Errorf("error code = %q, want %q", got.ErrorCode, "expired")
	}
	if h.totalSent() != 0 {
		t.Error("an expired job was pushed to a device")
	}
}

func TestJobExceedingMaxAttemptsIsFailed(t *testing.T) {
	st := storetest.New()
	h := newFakeHub(uuid.New())
	s := newScheduler(t, st, h, Options{MaxAttempts: 3})

	job := st.SeedJob(&store.Job{Recipient: "+1", Body: "x", Mode: store.ModeQueued, Attempts: 3})

	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got := st.JobByID(job.ID)
	if got.Status != store.JobFailed {
		t.Fatalf("status = %q, want %q", got.Status, store.JobFailed)
	}
	if got.ErrorCode != "too_many_attempts" {
		t.Errorf("error code = %q, want %q", got.ErrorCode, "too_many_attempts")
	}
}

// A device that went away between the commit and the push must not leave the
// job stuck assigned to a phone that never got it.
func TestJobIsReleasedWhenTheDeviceIsUnreachable(t *testing.T) {
	st := storetest.New()
	dev := uuid.New()
	h := newFakeHub(dev)
	h.sendErr[dev] = hub.ErrDeviceBusy
	s := newScheduler(t, st, h, Options{})

	job := st.SeedJob(&store.Job{Recipient: "+1", Body: "x", Mode: store.ModeQueued})

	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got := st.JobByID(job.ID)
	if got.Status != store.JobPending {
		t.Fatalf("status = %q, want %q", got.Status, store.JobPending)
	}
	if got.AssignedDeviceID != nil {
		t.Errorf("assigned device = %v, want nil after release", got.AssignedDeviceID)
	}
	// The attempt still counts: the point of the counter is to bound how often
	// we try, not how often we succeed.
	if got.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", got.Attempts)
	}
}

// Per-device throughput is the binding constraint, so a batch must not all land
// on one phone.
func TestLoadIsSpreadAcrossReadyDevices(t *testing.T) {
	st := storetest.New()
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	h := newFakeHub(a, b, c)
	s := newScheduler(t, st, h, Options{})

	for range 9 {
		st.SeedJob(&store.Job{Recipient: "+1", Body: "x", Mode: store.ModeQueued})
	}

	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	for _, d := range []uuid.UUID{a, b, c} {
		if n := len(h.sentTo(d)); n != 3 {
			t.Errorf("device %v got %d jobs, want 3", d, n)
		}
	}
}

func TestBatchSizeBoundsOneTick(t *testing.T) {
	st := storetest.New()
	h := newFakeHub(uuid.New())
	s := newScheduler(t, st, h, Options{BatchSize: 2})

	for range 5 {
		st.SeedJob(&store.Job{Recipient: "+1", Body: "x", Mode: store.ModeQueued})
	}

	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got := h.totalSent(); got != 2 {
		t.Errorf("sent %d jobs in one tick, want 2", got)
	}
}

func TestTerminalJobsAreNotReconsidered(t *testing.T) {
	st := storetest.New()
	h := newFakeHub(uuid.New())
	s := newScheduler(t, st, h, Options{})

	for _, status := range []store.JobStatus{store.JobSent, store.JobDelivered, store.JobFailed, store.JobCancelled, store.JobAssigned} {
		st.SeedJob(&store.Job{Recipient: "+1", Body: "x", Mode: store.ModeQueued, Status: status})
	}

	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got := h.totalSent(); got != 0 {
		t.Errorf("sent %d already-resolved jobs, want 0", got)
	}
}

func TestAssignmentIsAudited(t *testing.T) {
	st := storetest.New()
	dev := uuid.New()
	h := newFakeHub(dev)
	s := newScheduler(t, st, h, Options{})

	job := st.SeedJob(&store.Job{Recipient: "+1", Body: "x", Mode: store.ModeQueued})

	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	events := st.AllEvents()
	if len(events) != 1 {
		t.Fatalf("recorded %d events, want 1", len(events))
	}
	e := events[0]
	if e.JobID != job.ID {
		t.Errorf("event job = %v, want %v", e.JobID, job.ID)
	}
	if e.Status != store.JobAssigned {
		t.Errorf("event status = %q, want %q", e.Status, store.JobAssigned)
	}
	if e.DeviceID == nil || *e.DeviceID != dev {
		t.Errorf("event device = %v, want %v", e.DeviceID, dev)
	}
}

func TestTickPropagatesHubError(t *testing.T) {
	st := storetest.New()
	h := newFakeHub()
	sentinel := errors.New("hub is down")
	h.listErr = sentinel
	s := newScheduler(t, st, h, Options{})

	if err := s.Tick(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("Tick = %v, want %v", err, sentinel)
	}
}

func TestTickPropagatesStoreError(t *testing.T) {
	st := storetest.New()
	h := newFakeHub(uuid.New())
	s := newScheduler(t, st, h, Options{})

	sentinel := errors.New("database is down")
	st.FailNext(sentinel)

	if err := s.Tick(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("Tick = %v, want %v", err, sentinel)
	}
}

// A failing tick must not take the loop down: the next pass re-derives
// everything anyway.
func TestRunSurvivesAFailingTick(t *testing.T) {
	st := storetest.New()
	dev := uuid.New()
	h := newFakeHub(dev)
	s := newScheduler(t, st, h, Options{Interval: 5 * time.Millisecond})

	st.FailNext(errors.New("transient"))
	st.SeedJob(&store.Job{Recipient: "+1", Body: "x", Mode: store.ModeQueued})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.Run(ctx)
	}()

	deadline := time.After(3 * time.Second)
	for h.totalSent() == 0 {
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatal("scheduler never recovered from a failing tick")
		case <-time.After(5 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}
