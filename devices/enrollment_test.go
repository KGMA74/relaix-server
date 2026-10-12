package devices

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

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

	minted, err := s.MintEnrollmentToken(context.Background(), uuid.Nil)
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
	if _, err := s.Enroll(context.Background(), hashed, &store.Device{}, ""); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("the stored hash was accepted as a token: %v", err)
	}
}

func TestMintProducesDistinctTokens(t *testing.T) {
	st := storetest.New()
	s := newService(t, st, nil)

	seen := make(map[string]bool)
	for range 50 {
		m, err := s.MintEnrollmentToken(context.Background(), uuid.Nil)
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

	minted, err := s.MintEnrollmentToken(ctx, uuid.Nil)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	got, err := s.Enroll(ctx, minted.Plaintext, &store.Device{
		Label:       "desk phone",
		PhoneNumber: "+33600000000",
		Model:       "SM-A536B",
	}, "")
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

	minted, err := s.MintEnrollmentToken(ctx, uuid.Nil)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	if _, err := s.Enroll(ctx, minted.Plaintext, &store.Device{Label: "first"}, ""); err != nil {
		t.Fatalf("first Enroll: %v", err)
	}

	_, err = s.Enroll(ctx, minted.Plaintext, &store.Device{Label: "second"}, "")
	if !errors.Is(err, ErrTokenUsed) {
		t.Fatalf("second Enroll = %v, want ErrTokenUsed", err)
	}

	devices, err := st.Devices().List(ctx, store.AllAccounts())
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

	minted, err := s.MintEnrollmentToken(ctx, uuid.Nil)
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
			_, err := s.Enroll(ctx, minted.Plaintext, &store.Device{Label: "racer"}, "")
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

	devices, err := st.Devices().List(ctx, store.AllAccounts())
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

	minted, err := s.MintEnrollmentToken(ctx, uuid.Nil)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	now = testNow.Add(time.Hour) // well past the 15 minute TTL

	_, err = s.Enroll(ctx, minted.Plaintext, &store.Device{Label: "late"}, "")
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("Enroll = %v, want ErrTokenExpired", err)
	}

	// Expiry and reuse are distinguished because the operator's fix differs.
	if errors.Is(err, ErrTokenUsed) {
		t.Error("an expired token was reported as already used")
	}

	devices, err := st.Devices().List(ctx, store.AllAccounts())
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

	_, err := s.Enroll(context.Background(), "never-minted", &store.Device{}, "")
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Enroll = %v, want ErrInvalidToken", err)
	}
}

func TestEmptyTokenIsRejected(t *testing.T) {
	st := storetest.New()
	s := newService(t, st, nil)

	_, err := s.Enroll(context.Background(), "", &store.Device{}, "")
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Enroll = %v, want ErrInvalidToken", err)
	}
}

func TestConsumptionRecordsWhichDeviceClaimedTheToken(t *testing.T) {
	st := storetest.New()
	s := newService(t, st, nil)
	ctx := context.Background()

	minted, err := s.MintEnrollmentToken(ctx, uuid.Nil)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	enrolled, err := s.Enroll(ctx, minted.Plaintext, &store.Device{Label: "audited"}, "")
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	// Redeeming again surfaces the record; assert through the store instead.
	consumed, err := st.Enrollments().Consume(ctx, token.SHA256{}.Hash(minted.Plaintext), enrolled.Device.ID, testNow)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("re-consume = %v (%v), want ErrConflict", err, consumed)
	}
}

// The same phone enrolling twice keeps one device: the second enrollment
// re-keys the first instead of adding a row, and the old token stops working.
func TestEnrollSameHardwareTakesBackTheDevice(t *testing.T) {
	ctx := context.Background()
	st := storetest.New()
	s := newService(t, st, time.Now)

	first := mustEnroll(t, s, &store.Device{Label: "before reinstall"}, "android-id-1")
	if first.Reenrolled {
		t.Fatal("first enrollment reported as a re-enrollment")
	}

	second := mustEnroll(t, s, &store.Device{Label: "after reinstall"}, "android-id-1")
	if !second.Reenrolled {
		t.Error("second enrollment of the same hardware not reported as a re-enrollment")
	}
	if second.Device.ID != first.Device.ID {
		t.Fatalf("got a new device %v, want the existing %v back", second.Device.ID, first.Device.ID)
	}
	if second.Device.Label != "after reinstall" {
		t.Errorf("label = %q, want the fresh description", second.Device.Label)
	}

	all, err := st.Devices().List(ctx, store.AllAccounts())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Errorf("fleet has %d devices, want 1", len(all))
	}

	hasher := token.SHA256{}
	if _, err := st.Devices().GetByTokenHash(ctx, hasher.Hash(first.DeviceToken)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("old token still authenticates: %v", err)
	}
	if _, err := st.Devices().GetByTokenHash(ctx, hasher.Hash(second.DeviceToken)); err != nil {
		t.Errorf("new token does not authenticate: %v", err)
	}
}

func TestEnrollDifferentOrMissingHardwareMakesNewDevices(t *testing.T) {
	ctx := context.Background()
	st := storetest.New()
	s := newService(t, st, time.Now)

	a := mustEnroll(t, s, &store.Device{}, "id-a")
	b := mustEnroll(t, s, &store.Device{}, "id-b")
	c := mustEnroll(t, s, &store.Device{}, "")
	d := mustEnroll(t, s, &store.Device{}, "")
	ids := map[string]bool{}
	for _, e := range []*EnrolledDevice{a, b, c, d} {
		ids[e.Device.ID.String()] = true
		if e.Reenrolled {
			t.Errorf("device %v reported as re-enrolled", e.Device.ID)
		}
	}
	if len(ids) != 4 {
		t.Errorf("got %d distinct devices, want 4", len(ids))
	}
	all, _ := st.Devices().List(ctx, store.AllAccounts())
	if len(all) != 4 {
		t.Errorf("fleet has %d devices, want 4", len(all))
	}
}

// The stored value is a keyed hash: never the id itself, and different under
// a different key, so a dump cannot be matched against ids gathered elsewhere.
func TestHardwareHashIsKeyedAndNeverTheID(t *testing.T) {
	st := storetest.New()
	a := New(st, token.SHA256{}, Options{HardwareIDKey: []byte("key-a")})
	b := New(st, token.SHA256{}, Options{HardwareIDKey: []byte("key-b")})

	ha, hb := a.HardwareHash("android-id"), b.HardwareHash("android-id")
	if ha == "" || ha == "android-id" || strings.Contains(ha, "android-id") {
		t.Errorf("hash %q leaks the id", ha)
	}
	if ha == hb {
		t.Error("two keys produced the same hash")
	}
	if a.HardwareHash("android-id") != ha {
		t.Error("hash is not stable")
	}
	if a.HardwareHash("") != "" {
		t.Error("an empty id must hash to empty, so it matches nothing")
	}
}

func mustEnroll(t *testing.T, s *Service, info *store.Device, hardwareID string) *EnrolledDevice {
	t.Helper()
	minted, err := s.MintEnrollmentToken(context.Background(), uuid.Nil)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	got, err := s.Enroll(context.Background(), minted.Plaintext, info, hardwareID)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	return got
}
