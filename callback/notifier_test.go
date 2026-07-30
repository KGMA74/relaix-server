package callback

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/store"
)

var (
	testNow    = time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	testSecret = []byte("shared-secret")
)

func newNotifier(t *testing.T, opts Options) *Notifier {
	t.Helper()
	if opts.Secret == nil {
		opts.Secret = testSecret
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return testNow }
	}
	opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewNotifier(opts)
}

func terminalJob(url string) *store.Job {
	dev := uuid.New()
	completed := testNow
	return &store.Job{
		ID:               uuid.New(),
		Recipient:        "+33600000000",
		Body:             "hello",
		Mode:             store.ModeQueued,
		Status:           store.JobSent,
		PartsSent:        2,
		AssignedDeviceID: &dev,
		CompletedAt:      &completed,
		Callback:         store.CallbackState{URL: url},
	}
}

// capture records what the receiver saw.
type capture struct {
	mu        sync.Mutex
	body      []byte
	signature string
	timestamp string
	delivery  string
	calls     int
}

func (c *capture) record(r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.body, _ = io.ReadAll(r.Body)
	c.signature = r.Header.Get(HeaderSignature)
	c.timestamp = r.Header.Get(HeaderTimestamp)
	c.delivery = r.Header.Get(HeaderDelivery)
}

func TestNotifySendsSignedPayload(t *testing.T) {
	var got capture
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.record(r)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := newNotifier(t, Options{})
	job := terminalJob(srv.URL)

	if err := n.Notify(context.Background(), job); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	// The receiver must be able to authenticate what it got.
	if !Verify(testSecret, got.timestamp, got.body, got.signature) {
		t.Fatal("signature did not verify")
	}
	if got.timestamp != strconv.FormatInt(testNow.Unix(), 10) {
		t.Errorf("timestamp = %q, want %d", got.timestamp, testNow.Unix())
	}
	if got.delivery != job.ID.String() {
		t.Errorf("delivery header = %q, want %q", got.delivery, job.ID)
	}

	var p Payload
	if err := json.Unmarshal(got.body, &p); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	if p.JobID != job.ID.String() || p.Status != string(store.JobSent) {
		t.Errorf("payload = %+v", p)
	}
	if p.PartsSent != 2 {
		t.Errorf("partsSent = %d, want 2", p.PartsSent)
	}
	if p.Attempt != 1 {
		t.Errorf("attempt = %d, want 1 for the first delivery", p.Attempt)
	}
	if p.DeviceID == nil || *p.DeviceID != job.AssignedDeviceID.String() {
		t.Errorf("deviceId = %v", p.DeviceID)
	}
}

// The whole point of signing: anyone who guesses the URL cannot forge a
// callback, and nobody can edit one in flight.
func TestSignatureRejectsTamperingAndWrongSecret(t *testing.T) {
	body := []byte(`{"jobId":"abc","status":"sent"}`)
	stamp := "1780000000"
	sig := "sha256=" + Sign(testSecret, stamp, body)

	if !Verify(testSecret, stamp, body, sig) {
		t.Fatal("a genuine signature did not verify")
	}

	tests := []struct {
		name      string
		secret    []byte
		timestamp string
		body      []byte
	}{
		{"wrong secret", []byte("other-secret"), stamp, body},
		{"tampered body", testSecret, stamp, []byte(`{"jobId":"abc","status":"delivered"}`)},
		{"tampered timestamp", testSecret, "1790000000", body},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if Verify(tc.secret, tc.timestamp, tc.body, sig) {
				t.Error("signature verified when it should not have")
			}
		})
	}
}

// The timestamp is inside the signed material precisely so it cannot be moved
// to widen a replay window.
func TestTimestampIsCoveredBySignature(t *testing.T) {
	body := []byte(`{}`)
	a := Sign(testSecret, "1780000000", body)
	b := Sign(testSecret, "1780000001", body)
	if a == b {
		t.Fatal("changing the timestamp did not change the signature")
	}
}

func TestNotifyClassifiesResponses(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		wantErr   bool
		permanent bool
	}{
		{"200 accepted", http.StatusOK, false, false},
		{"204 accepted", http.StatusNoContent, false, false},
		{"400 permanent", http.StatusBadRequest, true, true},
		{"404 permanent", http.StatusNotFound, true, true},
		{"401 permanent", http.StatusUnauthorized, true, true},
		// Explicitly "come back later", so it must stay retryable.
		{"429 retryable", http.StatusTooManyRequests, true, false},
		{"500 retryable", http.StatusInternalServerError, true, false},
		{"503 retryable", http.StatusServiceUnavailable, true, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			n := newNotifier(t, Options{})
			err := n.Notify(context.Background(), terminalJob(srv.URL))

			if tc.wantErr && err == nil {
				t.Fatalf("status %d returned no error", tc.status)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("status %d returned %v", tc.status, err)
			}
			if got := errors.Is(err, ErrPermanent); got != tc.permanent {
				t.Errorf("permanent = %v, want %v (err %v)", got, tc.permanent, err)
			}
		})
	}
}

// A receiver that is down is worth retrying; that must not be reported as
// permanent.
func TestUnreachableReceiverIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing listening now

	n := newNotifier(t, Options{})
	err := n.Notify(context.Background(), terminalJob(url))
	if err == nil {
		t.Fatal("posting to a closed server succeeded")
	}
	if errors.Is(err, ErrPermanent) {
		t.Errorf("an unreachable receiver was treated as permanent: %v", err)
	}
}

func TestMalformedURLIsPermanent(t *testing.T) {
	n := newNotifier(t, Options{})
	job := terminalJob("://not a url")

	err := n.Notify(context.Background(), job)
	if !errors.Is(err, ErrPermanent) {
		t.Fatalf("Notify = %v, want ErrPermanent", err)
	}
}

func TestNoCallbackURLIsANoOp(t *testing.T) {
	n := newNotifier(t, Options{})
	if err := n.Notify(context.Background(), terminalJob("")); err != nil {
		t.Fatalf("Notify with no URL = %v, want nil", err)
	}
}

func TestSlowReceiverTimesOut(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer func() { close(release); srv.Close() }()

	n := newNotifier(t, Options{Timeout: 100 * time.Millisecond})
	err := n.Notify(context.Background(), terminalJob(srv.URL))
	if err == nil {
		t.Fatal("a hung receiver did not time out")
	}
	// One hung endpoint must not be treated as a permanent rejection.
	if errors.Is(err, ErrPermanent) {
		t.Errorf("a timeout was treated as permanent: %v", err)
	}
}

func TestAttemptCountIsReported(t *testing.T) {
	var got capture
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.record(r)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := newNotifier(t, Options{})
	job := terminalJob(srv.URL)
	job.Callback.Attempts = 3

	if err := n.Notify(context.Background(), job); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	var p Payload
	if err := json.Unmarshal(got.body, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.Attempt != 4 {
		t.Errorf("attempt = %d, want 4 after 3 failures", p.Attempt)
	}
}
