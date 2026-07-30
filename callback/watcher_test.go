package callback

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/store"
	"github.com/KGMA74/relaix-server/store/storetest"
)

// fakeSender records deliveries and answers with scripted errors.
type fakeSender struct {
	mu    sync.Mutex
	calls []uuid.UUID
	errs  []error // consumed in order; nil once exhausted
}

func (f *fakeSender) Notify(_ context.Context, job *store.Job) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, job.ID)
	if len(f.errs) == 0 {
		return nil
	}
	err := f.errs[0]
	f.errs = f.errs[1:]
	return err
}

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func newWatcher(t *testing.T, st store.Store, s Sender, opts WatcherOptions) *Watcher {
	t.Helper()
	if opts.Now == nil {
		opts.Now = func() time.Time { return testNow }
	}
	opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	// Pin the jitter so delays are exact; randomness has its own test.
	if opts.rand == nil {
		opts.rand = func() float64 { return 0 }
	}
	return NewWatcher(st, s, opts)
}

// dueJob seeds a job whose callback is owed and due.
func dueJob(st *storetest.Store, attempts int) *store.Job {
	due := testNow.Add(-time.Minute)
	return st.SeedJob(&store.Job{
		Recipient: "+33600000000",
		Body:      "x",
		Mode:      store.ModeQueued,
		Status:    store.JobSent,
		Callback: store.CallbackState{
			URL:           "https://example.test/cb",
			Attempts:      attempts,
			NextAttemptAt: &due,
		},
	})
}

func TestTickDeliversAndMarksDone(t *testing.T) {
	st := storetest.New()
	sender := &fakeSender{}
	w := newWatcher(t, st, sender, WatcherOptions{})

	job := dueJob(st, 0)

	if err := w.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if sender.count() != 1 {
		t.Fatalf("sent %d callbacks, want 1", sender.count())
	}

	got := st.JobByID(job.ID)
	if got.Callback.DeliveredAt == nil {
		t.Error("callback was not marked delivered")
	}
	if got.Callback.NextAttemptAt != nil {
		t.Error("a delivered callback is still scheduled")
	}

	// A delivered callback must never be picked up again.
	if err := w.Tick(context.Background()); err != nil {
		t.Fatalf("second Tick: %v", err)
	}
	if sender.count() != 1 {
		t.Errorf("a delivered callback was retried: %d sends", sender.count())
	}
}

func TestFailureSchedulesARetry(t *testing.T) {
	st := storetest.New()
	sender := &fakeSender{errs: []error{errors.New("connection refused")}}
	w := newWatcher(t, st, sender, WatcherOptions{BaseDelay: 30 * time.Second})

	job := dueJob(st, 0)

	if err := w.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got := st.JobByID(job.ID)
	if got.Callback.DeliveredAt != nil {
		t.Fatal("a failed callback was marked delivered")
	}
	if got.Callback.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", got.Callback.Attempts)
	}
	if got.Callback.LastError == "" {
		t.Error("the failure reason was not recorded")
	}
	if got.Callback.NextAttemptAt == nil {
		t.Fatal("no retry scheduled")
	}
	want := testNow.Add(30 * time.Second)
	if !got.Callback.NextAttemptAt.Equal(want) {
		t.Errorf("next attempt at %v, want %v", got.Callback.NextAttemptAt, want)
	}
}

// Doubling, so a receiver down for an hour is not hammered thousands of times.
func TestBackoffDoublesAndIsCapped(t *testing.T) {
	st := storetest.New()
	w := newWatcher(t, st, &fakeSender{}, WatcherOptions{
		BaseDelay: time.Second,
		MaxDelay:  10 * time.Second,
	})

	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{1, time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 10 * time.Second}, // capped
		{9, 10 * time.Second},
	}
	for _, tc := range tests {
		if got := w.backoff(tc.attempt); got != tc.want {
			t.Errorf("backoff(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

// A huge attempt count must not overflow into a negative delay, which would
// schedule the retry in the past and spin.
func TestBackoffSurvivesAbsurdAttemptCounts(t *testing.T) {
	st := storetest.New()
	w := newWatcher(t, st, &fakeSender{}, WatcherOptions{
		BaseDelay: time.Second,
		MaxDelay:  time.Hour,
	})

	for _, attempt := range []int{0, 1, 64, 1000, 1 << 20} {
		got := w.backoff(attempt)
		if got <= 0 {
			t.Errorf("backoff(%d) = %v, want a positive delay", attempt, got)
		}
		if got > time.Hour {
			t.Errorf("backoff(%d) = %v, exceeds MaxDelay", attempt, got)
		}
	}
}

// Without jitter, everything that failed together retries together forever.
func TestJitterSpreadsRetries(t *testing.T) {
	st := storetest.New()
	seq := []float64{0, 0.25, 0.5, 0.75, 1}
	i := 0
	w := newWatcher(t, st, &fakeSender{}, WatcherOptions{
		BaseDelay: 100 * time.Second,
		MaxDelay:  time.Hour,
		Jitter:    0.5,
		rand: func() float64 {
			v := seq[i%len(seq)]
			i++
			return v
		},
	})

	seen := make(map[time.Duration]bool)
	for range len(seq) {
		d := w.backoff(1)
		seen[d] = true
		// Jitter only shortens, so the cap stays a cap.
		if d > 100*time.Second {
			t.Errorf("jittered delay %v exceeds the base delay", d)
		}
		if d <= 0 {
			t.Errorf("jittered delay %v is not positive", d)
		}
	}
	if len(seen) < 2 {
		t.Error("jitter produced identical delays; a stampede would survive it")
	}
}

// A receiver answering 400 will answer 400 in an hour too.
func TestPermanentFailureIsAbandonedImmediately(t *testing.T) {
	st := storetest.New()
	sender := &fakeSender{errs: []error{fmt.Errorf("%w: status 400", ErrPermanent)}}
	w := newWatcher(t, st, sender, WatcherOptions{MaxAttempts: 10})

	job := dueJob(st, 0)

	if err := w.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// It must never be claimed again.
	if err := w.Tick(context.Background()); err != nil {
		t.Fatalf("second Tick: %v", err)
	}
	if sender.count() != 1 {
		t.Errorf("a permanently rejected callback was retried: %d sends", sender.count())
	}

	got := st.JobByID(job.ID)
	if got.Callback.LastError == "" {
		t.Error("the rejection was not recorded for inspection")
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	st := storetest.New()
	sender := &fakeSender{errs: []error{errors.New("still down")}}
	w := newWatcher(t, st, sender, WatcherOptions{MaxAttempts: 3})

	// Already failed twice; this attempt is the third and last.
	job := dueJob(st, 2)

	if err := w.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if err := w.Tick(context.Background()); err != nil {
		t.Fatalf("second Tick: %v", err)
	}
	if sender.count() != 1 {
		t.Errorf("retried past MaxAttempts: %d sends", sender.count())
	}

	got := st.JobByID(job.ID)
	if got.Callback.LastError == "" {
		t.Error("the final failure was not recorded")
	}
}

func TestJobsWithoutACallbackAreIgnored(t *testing.T) {
	st := storetest.New()
	sender := &fakeSender{}
	w := newWatcher(t, st, sender, WatcherOptions{})

	st.SeedJob(&store.Job{
		Recipient: "+1", Body: "x", Mode: store.ModeQueued, Status: store.JobSent,
	})

	if err := w.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if sender.count() != 0 {
		t.Errorf("sent %d callbacks for a job with no URL, want 0", sender.count())
	}
}

func TestNotYetDueCallbacksAreLeftAlone(t *testing.T) {
	st := storetest.New()
	sender := &fakeSender{}
	w := newWatcher(t, st, sender, WatcherOptions{})

	future := testNow.Add(time.Hour)
	st.SeedJob(&store.Job{
		Recipient: "+1", Body: "x", Mode: store.ModeQueued, Status: store.JobSent,
		Callback: store.CallbackState{URL: "https://example.test/cb", NextAttemptAt: &future},
	})

	if err := w.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if sender.count() != 0 {
		t.Errorf("sent %d callbacks before they were due, want 0", sender.count())
	}
}

func TestBatchSizeBoundsOnePoll(t *testing.T) {
	st := storetest.New()
	sender := &fakeSender{}
	w := newWatcher(t, st, sender, WatcherOptions{BatchSize: 2})

	for range 5 {
		dueJob(st, 0)
	}

	if err := w.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if sender.count() != 2 {
		t.Errorf("sent %d callbacks in one poll, want 2", sender.count())
	}
}

func TestDeliveryIsAudited(t *testing.T) {
	st := storetest.New()
	w := newWatcher(t, st, &fakeSender{}, WatcherOptions{})
	job := dueJob(st, 0)

	if err := w.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	events := st.AllEvents()
	if len(events) != 1 {
		t.Fatalf("recorded %d events, want 1", len(events))
	}
	if events[0].JobID != job.ID {
		t.Errorf("event job = %v, want %v", events[0].JobID, job.ID)
	}
}

func TestTickPropagatesStoreError(t *testing.T) {
	st := storetest.New()
	w := newWatcher(t, st, &fakeSender{}, WatcherOptions{})

	sentinel := errors.New("database is down")
	st.FailNext(sentinel)

	if err := w.Tick(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("Tick = %v, want %v", err, sentinel)
	}
}

// A failing poll must not take the loop down.
func TestRunSurvivesAFailingTick(t *testing.T) {
	st := storetest.New()
	sender := &fakeSender{}
	w := newWatcher(t, st, sender, WatcherOptions{Interval: 5 * time.Millisecond})

	st.FailNext(errors.New("transient"))
	dueJob(st, 0)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = w.Run(ctx) }()

	deadline := time.After(3 * time.Second)
	for sender.count() == 0 {
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatal("watcher never recovered from a failing poll")
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
