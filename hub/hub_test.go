package hub

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
	"github.com/KGMA74/relaix-server/store"
)

// fakeClock lets the tests move time without sleeping.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// start brings up a hub for a test and tears it down afterwards.
func start(t *testing.T, opts Options) (*Hub, *fakeClock, context.Context) {
	t.Helper()

	clock := &fakeClock{t: time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)}
	opts.Now = clock.now
	opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))

	h := New(opts)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = h.Run(ctx)
	}()

	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("hub Run did not return after cancellation")
		}
	})

	return h, clock, ctx
}

// healthy is a snapshot of a device fit to send.
func healthy() *store.DeviceHealth {
	return &store.DeviceHealth{
		BatteryLevel:   80,
		IsCharging:     false,
		SignalStrength: 4,
		NetworkType:    "LTE",
		SimReady:       true,
		PermissionsOK:  true,
	}
}

func TestRegisterThenGet(t *testing.T) {
	h, clock, ctx := start(t, Options{})
	id := uuid.New()

	reg, err := h.Register(ctx, id, healthy())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, err := h.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.DeviceID != id {
		t.Errorf("DeviceID = %v, want %v", got.DeviceID, id)
	}
	if got.RegistrationID != reg.ID {
		t.Errorf("RegistrationID = %v, want %v", got.RegistrationID, reg.ID)
	}
	if !got.LastSeenAt.Equal(clock.now()) {
		t.Errorf("LastSeenAt = %v, want %v", got.LastSeenAt, clock.now())
	}
}

func TestGetUnknownDeviceIsNotConnected(t *testing.T) {
	h, _, ctx := start(t, Options{})

	_, err := h.Get(ctx, uuid.New())
	if !errors.Is(err, ErrNotConnected) {
		t.Fatalf("Get unknown = %v, want ErrNotConnected", err)
	}
}

// A reconnecting device must supersede its old stream, and the old stream's
// handler must learn about it by seeing its channel closed.
func TestRegisterEvictsPreviousConnection(t *testing.T) {
	h, _, ctx := start(t, Options{})
	id := uuid.New()

	first, err := h.Register(ctx, id, healthy())
	if err != nil {
		t.Fatalf("first Register: %v", err)
	}
	second, err := h.Register(ctx, id, healthy())
	if err != nil {
		t.Fatalf("second Register: %v", err)
	}

	select {
	case _, open := <-first.Out:
		if open {
			t.Fatal("first connection received a message; want closed channel")
		}
	case <-time.After(time.Second):
		t.Fatal("first connection channel was not closed on eviction")
	}

	got, err := h.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.RegistrationID != second.ID {
		t.Errorf("current registration = %v, want the second one %v", got.RegistrationID, second.ID)
	}
}

// The bug this guards: a replaced stream tearing down late must not evict the
// connection that took its place.
func TestUnregisterWithStaleRegistrationIsIgnored(t *testing.T) {
	h, _, ctx := start(t, Options{})
	id := uuid.New()

	first, err := h.Register(ctx, id, healthy())
	if err != nil {
		t.Fatalf("first Register: %v", err)
	}
	second, err := h.Register(ctx, id, healthy())
	if err != nil {
		t.Fatalf("second Register: %v", err)
	}

	if err := h.Unregister(ctx, id, first.ID, "late teardown"); err != nil {
		t.Fatalf("Unregister: %v", err)
	}

	got, err := h.Get(ctx, id)
	if err != nil {
		t.Fatalf("device was evicted by a stale unregister: %v", err)
	}
	if got.RegistrationID != second.ID {
		t.Errorf("registration = %v, want %v", got.RegistrationID, second.ID)
	}
}

func TestUnregisterRemovesAndClosesChannel(t *testing.T) {
	h, _, ctx := start(t, Options{})
	id := uuid.New()

	reg, err := h.Register(ctx, id, healthy())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := h.Unregister(ctx, id, reg.ID, "stream closed"); err != nil {
		t.Fatalf("Unregister: %v", err)
	}

	select {
	case _, open := <-reg.Out:
		if open {
			t.Fatal("channel delivered a message; want closed")
		}
	case <-time.After(time.Second):
		t.Fatal("channel was not closed on unregister")
	}

	if _, err := h.Get(ctx, id); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("Get after Unregister = %v, want ErrNotConnected", err)
	}
}

func TestHeartbeatRefreshesLivenessAndHealth(t *testing.T) {
	h, clock, ctx := start(t, Options{HeartbeatTTL: time.Minute})
	id := uuid.New()

	reg, err := h.Register(ctx, id, healthy())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	clock.advance(30 * time.Second)
	next := healthy()
	next.BatteryLevel = 42
	if err := h.Heartbeat(ctx, id, reg.ID, next); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	got, err := h.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.LastSeenAt.Equal(clock.now()) {
		t.Errorf("LastSeenAt = %v, want %v", got.LastSeenAt, clock.now())
	}
	if got.Health.BatteryLevel != 42 {
		t.Errorf("BatteryLevel = %d, want 42", got.Health.BatteryLevel)
	}
}

func TestHeartbeatFromStaleRegistrationIsRejected(t *testing.T) {
	h, _, ctx := start(t, Options{})
	id := uuid.New()

	first, err := h.Register(ctx, id, healthy())
	if err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if _, err := h.Register(ctx, id, healthy()); err != nil {
		t.Fatalf("second Register: %v", err)
	}

	if err := h.Heartbeat(ctx, id, first.ID, healthy()); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("stale Heartbeat = %v, want ErrNotConnected", err)
	}
}

// The snapshot handed to callers must not alias hub state.
func TestSnapshotDoesNotAliasHubState(t *testing.T) {
	h, _, ctx := start(t, Options{})
	id := uuid.New()

	in := healthy()
	if _, err := h.Register(ctx, id, in); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Mutating what we passed in must not reach the hub.
	in.BatteryLevel = 1

	got, err := h.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Health.BatteryLevel != 80 {
		t.Errorf("hub state was aliased by the caller's struct: battery = %d, want 80", got.Health.BatteryLevel)
	}

	// Nor must mutating the snapshot reach it.
	got.Health.BatteryLevel = 2
	again, err := h.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if again.Health.BatteryLevel != 80 {
		t.Errorf("hub state was aliased by the returned snapshot: battery = %d, want 80", again.Health.BatteryLevel)
	}
}

func TestSendJobDelivers(t *testing.T) {
	h, _, ctx := start(t, Options{})
	id := uuid.New()

	reg, err := h.Register(ctx, id, healthy())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	msg := &v1.ServerMessage{MessageId: "m1"}
	if err := h.SendJob(ctx, id, msg); err != nil {
		t.Fatalf("SendJob: %v", err)
	}

	select {
	case got := <-reg.Out:
		if got.GetMessageId() != "m1" {
			t.Errorf("MessageId = %q, want %q", got.GetMessageId(), "m1")
		}
	case <-time.After(time.Second):
		t.Fatal("message was not delivered")
	}
}

func TestSendJobToDisconnectedDevice(t *testing.T) {
	h, _, ctx := start(t, Options{})

	err := h.SendJob(ctx, uuid.New(), &v1.ServerMessage{})
	if !errors.Is(err, ErrNotConnected) {
		t.Fatalf("SendJob = %v, want ErrNotConnected", err)
	}
}

// The central invariant: one phone that stops reading must not stall the hub.
func TestSendJobOnFullBufferReportsBusyAndDoesNotBlock(t *testing.T) {
	const buffer = 2
	h, _, ctx := start(t, Options{OutboundBuffer: buffer})
	id := uuid.New()

	if _, err := h.Register(ctx, id, healthy()); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Nobody is reading Out, so the buffer fills.
	for range buffer {
		if err := h.SendJob(ctx, id, &v1.ServerMessage{}); err != nil {
			t.Fatalf("SendJob: %v", err)
		}
	}

	done := make(chan error, 1)
	go func() { done <- h.SendJob(ctx, id, &v1.ServerMessage{}) }()

	select {
	case err := <-done:
		if !errors.Is(err, ErrDeviceBusy) {
			t.Fatalf("SendJob on full buffer = %v, want ErrDeviceBusy", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SendJob blocked on a full buffer; the hub goroutine is stalled")
	}

	// The hub must still answer other callers.
	if _, err := h.Get(ctx, id); err != nil {
		t.Fatalf("hub unresponsive after a busy device: %v", err)
	}

	got, err := h.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Busy {
		t.Error("device was not marked busy")
	}
}

func TestHeartbeatClearsBusy(t *testing.T) {
	h, _, ctx := start(t, Options{OutboundBuffer: 1})
	id := uuid.New()

	reg, err := h.Register(ctx, id, healthy())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := h.SendJob(ctx, id, &v1.ServerMessage{}); err != nil {
		t.Fatalf("SendJob: %v", err)
	}
	if err := h.SendJob(ctx, id, &v1.ServerMessage{}); !errors.Is(err, ErrDeviceBusy) {
		t.Fatalf("second SendJob = %v, want ErrDeviceBusy", err)
	}

	// The agent catches up and heartbeats.
	<-reg.Out
	if err := h.Heartbeat(ctx, id, reg.ID, healthy()); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	got, err := h.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Busy {
		t.Error("Busy was not cleared by a heartbeat")
	}
}

func TestListReady(t *testing.T) {
	tests := []struct {
		name   string
		health *store.DeviceHealth
		want   bool
	}{
		{"healthy", healthy(), true},
		{"never reported health", nil, false},
		{
			name: "no signal",
			health: func() *store.DeviceHealth {
				h := healthy()
				h.SignalStrength = 0
				return h
			}(),
			want: false,
		},
		{
			name: "sim not ready",
			health: func() *store.DeviceHealth {
				h := healthy()
				h.SimReady = false
				return h
			}(),
			want: false,
		},
		{
			name: "permissions revoked",
			health: func() *store.DeviceHealth {
				h := healthy()
				h.PermissionsOK = false
				return h
			}(),
			want: false,
		},
		{
			name: "battery low and unplugged",
			health: func() *store.DeviceHealth {
				h := healthy()
				h.BatteryLevel = 5
				h.IsCharging = false
				return h
			}(),
			want: false,
		},
		{
			name: "battery low but charging",
			health: func() *store.DeviceHealth {
				h := healthy()
				h.BatteryLevel = 5
				h.IsCharging = true
				return h
			}(),
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _, ctx := start(t, Options{MinBattery: 15})
			id := uuid.New()
			if _, err := h.Register(ctx, id, tc.health); err != nil {
				t.Fatalf("Register: %v", err)
			}

			ready, err := h.ListReady(ctx)
			if err != nil {
				t.Fatalf("ListReady: %v", err)
			}
			if got := len(ready) == 1; got != tc.want {
				t.Errorf("ready = %v, want %v (got %d devices)", got, tc.want, len(ready))
			}
		})
	}
}

// A phone that drove into a tunnel often leaves a socket that still looks fine,
// so readiness follows heartbeats rather than the connection.
func TestListReadyExcludesStaleHeartbeat(t *testing.T) {
	h, clock, ctx := start(t, Options{HeartbeatTTL: time.Minute})
	id := uuid.New()

	reg, err := h.Register(ctx, id, healthy())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	clock.advance(90 * time.Second)
	ready, err := h.ListReady(ctx)
	if err != nil {
		t.Fatalf("ListReady: %v", err)
	}
	if len(ready) != 0 {
		t.Fatalf("stale device is still ready: %d devices", len(ready))
	}

	// It comes back the moment it is heard from again.
	if err := h.Heartbeat(ctx, id, reg.ID, healthy()); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	ready, err = h.ListReady(ctx)
	if err != nil {
		t.Fatalf("ListReady: %v", err)
	}
	if len(ready) != 1 {
		t.Fatalf("device did not become ready after a heartbeat: %d devices", len(ready))
	}
}

func TestListReadyExcludesBusy(t *testing.T) {
	h, _, ctx := start(t, Options{OutboundBuffer: 1})
	id := uuid.New()

	if _, err := h.Register(ctx, id, healthy()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := h.SendJob(ctx, id, &v1.ServerMessage{}); err != nil {
		t.Fatalf("SendJob: %v", err)
	}
	if err := h.SendJob(ctx, id, &v1.ServerMessage{}); !errors.Is(err, ErrDeviceBusy) {
		t.Fatalf("SendJob = %v, want ErrDeviceBusy", err)
	}

	ready, err := h.ListReady(ctx)
	if err != nil {
		t.Fatalf("ListReady: %v", err)
	}
	if len(ready) != 0 {
		t.Errorf("busy device is still ready: %d devices", len(ready))
	}
}

func TestListConnectedIncludesUnreadyDevices(t *testing.T) {
	h, _, ctx := start(t, Options{})

	ready := uuid.New()
	if _, err := h.Register(ctx, ready, healthy()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	unready := uuid.New()
	if _, err := h.Register(ctx, unready, nil); err != nil {
		t.Fatalf("Register: %v", err)
	}

	all, err := h.ListConnected(ctx)
	if err != nil {
		t.Fatalf("ListConnected: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("ListConnected returned %d devices, want 2", len(all))
	}
}

// Stopping the hub must tell every connected handler to shut down.
func TestRunClosesAllChannelsOnShutdown(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	h := New(Options{Now: clock.now, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = h.Run(ctx)
	}()

	reg, err := h.Register(ctx, uuid.New(), healthy())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	cancel()
	<-done

	select {
	case _, open := <-reg.Out:
		if open {
			t.Fatal("channel delivered a message; want closed")
		}
	default:
		t.Fatal("channel was not closed when the hub stopped")
	}
}

// Calls made after the hub stops must fail rather than hang forever.
func TestCallsAfterStopReturnErrStopped(t *testing.T) {
	h := New(Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = h.Run(ctx)
	}()
	cancel()
	<-done

	errCh := make(chan error, 1)
	go func() { _, err := h.Get(context.Background(), uuid.New()); errCh <- err }()

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrStopped) {
			t.Fatalf("Get after stop = %v, want ErrStopped", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Get blocked forever after the hub stopped")
	}
}

// Hammer every operation concurrently. Run with -race, this is what proves the
// actor pattern is actually keeping the state to one goroutine.
func TestConcurrentOperations(t *testing.T) {
	h, _, ctx := start(t, Options{OutboundBuffer: 4})

	const devices, iterations = 8, 100
	ids := make([]uuid.UUID, devices)
	for i := range ids {
		ids[i] = uuid.New()
	}

	// Drain outbound channels so senders are not merely hitting a full buffer.
	var drainers sync.WaitGroup
	stopDrain := make(chan struct{})

	var wg sync.WaitGroup
	for _, id := range ids {
		reg, err := h.Register(ctx, id, healthy())
		if err != nil {
			t.Fatalf("Register: %v", err)
		}

		drainers.Add(1)
		go func(out <-chan *v1.ServerMessage) {
			defer drainers.Done()
			for {
				select {
				case _, open := <-out:
					if !open {
						return
					}
				case <-stopDrain:
					return
				}
			}
		}(reg.Out)

		wg.Add(4)
		go func(id, regID uuid.UUID) {
			defer wg.Done()
			for range iterations {
				_ = h.Heartbeat(ctx, id, regID, healthy())
			}
		}(id, reg.ID)
		go func(id uuid.UUID) {
			defer wg.Done()
			for range iterations {
				_ = h.SendJob(ctx, id, &v1.ServerMessage{})
			}
		}(id)
		go func(id uuid.UUID) {
			defer wg.Done()
			for range iterations {
				_, _ = h.Get(ctx, id)
			}
		}(id)
		go func() {
			defer wg.Done()
			for range iterations {
				_, _ = h.ListReady(ctx)
			}
		}()
	}

	waited := make(chan struct{})
	go func() { wg.Wait(); close(waited) }()

	select {
	case <-waited:
	case <-time.After(30 * time.Second):
		t.Fatal("concurrent operations deadlocked")
	}

	close(stopDrain)
	drainers.Wait()

	all, err := h.ListConnected(ctx)
	if err != nil {
		t.Fatalf("ListConnected: %v", err)
	}
	if len(all) != devices {
		t.Errorf("ListConnected returned %d devices, want %d", len(all), devices)
	}
}
