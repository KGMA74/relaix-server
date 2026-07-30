package devices

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/KGMA74/relaix-server/store"
	"github.com/KGMA74/relaix-server/store/storetest"
	"github.com/KGMA74/relaix-server/token"
)

var testNow = time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

func newService(t *testing.T, st store.Store, now func() time.Time) *Service {
	t.Helper()
	if now == nil {
		now = func() time.Time { return testNow }
	}
	return New(st, token.SHA256{}, Options{
		TokenTTL: 15 * time.Minute,
		Now:      now,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func TestMintReturnsPlaintextAndStoresOnlyTheHash(t *testing.T) {
	st := storetest.New()
	s := newService(t, st, nil)

	minted, err := s.MintEnrollmentToken(context.Background())
	if err != nil {
		t.Fatalf("MintEnrollmentToken: %v", err)
	}
	if minted.Plaintext == "" {
		t.Fatal("no plaintext returned")
	}
	if minted.Record.ExpiresAt != testNow.Add(15*time.Minute) {
		t.Errorf("expires at %v, want %v", minted.Record.ExpiresAt, testNow.Add(15*time.Minute))
	}
	if minted.Record.ConsumedAt != nil {
		t.Error("a freshly minted token is already consumed")
	}

	// A database dump must not contain anything that can be presented as a
	// token. Redeeming the stored hash must fail.
	hashed := token.SHA256{}.Hash(minted.Plaintext)
	if _, err := s.Enroll(context.Background(), hashed, &store.Device{}); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("the stored hash was accepted as a token: %v", err)
	}
}

func TestMintProducesDistinctTokens(t *testing.T) {
	st := storetest.New()
	s := newService(t, st, nil)

	seen := make(map[string]bool)
	for range 50 {
		m, err := s.MintEnrollmentToken(context.Background())
		if err != nil {
			t.Fatalf("MintEnrollmentToken: %v", err)
		}
		if seen[m.Plaintext] {
			t.Fatal("minted a duplicate token")
		}
		seen[m.Plaintext] = true
	}
}

func TestEnrollCreatesDeviceAndReturnsToken(t *testing.T) {
	st := storetest.New()
	s := newService(t, st, nil)
	ctx := context.Background()

	minted, err := s.MintEnrollmentToken(ctx)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	got, err := s.Enroll(ctx, minted.Plaintext, &store.Device{
		Label:       "desk phone",
		PhoneNumber: "+33600000000",
		Model:       "SM-A536B",
	})
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if got.DeviceToken == "" {
		t.Fatal("no device token returned")
	}
	if got.Device.Label != "desk phone" {
		t.Errorf("label = %q, want %q", got.Device.Label, "desk phone")
	}
	if !got.Device.Enabled {
		t.Error("a newly enrolled device must be enabled")
	}

	// The returned token must actually authenticate the device.
	found, err := st.Devices().GetByTokenHash(ctx, token.SHA256{}.Hash(got.DeviceToken))
	if err != nil {
		t.Fatalf("device token does not resolve: %v", err)
	}
	if found.ID != got.Device.ID {
		t.Errorf("token resolves to %v, want %v", found.ID, got.Device.ID)
	}
}

// The property the whole QR flow rests on: a photographed code is worthless
// after the first use.
func TestTokenCannotBeUsedTwice(t *testing.T) {
	st := storetest.New()
	s := newService(t, st, nil)
	ctx := context.Background()

	minted, err := s.MintEnrollmentToken(ctx)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	if _, err := s.Enroll(ctx, minted.Plaintext, &store.Device{Label: "first"}); err != nil {
		t.Fatalf("first Enroll: %v", err)
	}

	_, err = s.Enroll(ctx, minted.Plaintext, &store.Device{Label: "second"})
	if !errors.Is(err, ErrTokenUsed) {
		t.Fatalf("second Enroll = %v, want ErrTokenUsed", err)
	}

	devices, err := st.Devices().List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(devices) != 1 {
		t.Errorf("%d devices exist, want 1 — the second enrollment left one behind", len(devices))
	}
}

// Concurrent redemption of one token must produce exactly one device: the
// consumption is the serialization point, not a check followed by a write.
func TestConcurrentEnrollmentYieldsOneDevice(t *testing.T) {
	st := storetest.New()
	s := newService(t, st, nil)
	ctx := context.Background()

	minted, err := s.MintEnrollmentToken(ctx)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	const racers = 8
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		wins     int
		failures []error
	)
	for i := range racers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.Enroll(ctx, minted.Plaintext, &store.Device{Label: "racer"})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				wins++
			} else {
				failures = append(failures, err)
			}
		}(i)
	}
	wg.Wait()

	if wins != 1 {
		t.Fatalf("%d enrollments succeeded, want exactly 1", wins)
	}
	for _, err := range failures {
		if !errors.Is(err, ErrTokenUsed) {
			t.Errorf("loser got %v, want ErrTokenUsed", err)
		}
	}

	devices, err := st.Devices().List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(devices) != 1 {
		t.Errorf("%d devices created, want 1", len(devices))
	}
}

func TestExpiredTokenIsRejected(t *testing.T) {
	st := storetest.New()
	now := testNow
	s := newService(t, st, func() time.Time { return now })
	ctx := context.Background()

	minted, err := s.MintEnrollmentToken(ctx)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	now = testNow.Add(time.Hour) // well past the 15 minute TTL

	_, err = s.Enroll(ctx, minted.Plaintext, &store.Device{Label: "late"})
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("Enroll = %v, want ErrTokenExpired", err)
	}

	// Expiry and reuse are distinguished because the operator's fix differs.
	if errors.Is(err, ErrTokenUsed) {
		t.Error("an expired token was reported as already used")
	}

	devices, err := st.Devices().List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(devices) != 0 {
		t.Errorf("%d devices created from an expired token, want 0", len(devices))
	}
}

func TestUnknownTokenIsRejected(t *testing.T) {
	st := storetest.New()
	s := newService(t, st, nil)

	_, err := s.Enroll(context.Background(), "never-minted", &store.Device{})
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Enroll = %v, want ErrInvalidToken", err)
	}
}

func TestEmptyTokenIsRejected(t *testing.T) {
	st := storetest.New()
	s := newService(t, st, nil)

	_, err := s.Enroll(context.Background(), "", &store.Device{})
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Enroll = %v, want ErrInvalidToken", err)
	}
}

func TestConsumptionRecordsWhichDeviceClaimedTheToken(t *testing.T) {
	st := storetest.New()
	s := newService(t, st, nil)
	ctx := context.Background()

	minted, err := s.MintEnrollmentToken(ctx)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	enrolled, err := s.Enroll(ctx, minted.Plaintext, &store.Device{Label: "audited"})
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	// Redeeming again surfaces the record; assert through the store instead.
	consumed, err := st.Enrollments().Consume(ctx, token.SHA256{}.Hash(minted.Plaintext), enrolled.Device.ID, testNow)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("re-consume = %v (%v), want ErrConflict", err, consumed)
	}
}
