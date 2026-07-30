// Package hub keeps the in-memory registry of connected devices.
//
// All of that state is owned by a single goroutine and reachable only by
// sending it a command; nothing else touches the map. The reason is that the
// operations that matter are composite — "find a ready device and hand it this
// job" is a read followed by a write that must not interleave with another
// scheduler pass — and a mutex makes each access safe without making the
// decision atomic. With one owning goroutine those become ordinary sequential
// code, with no lock ordering to reason about and an explicit serialization
// point. See docs/architecture.md §4 in the monorepo.
//
// The cost of that choice is that every operation is serialized through one
// goroutine, so the rule is absolute: no command may block. Sends to a device's
// outbound channel are non-blocking against a bounded buffer, and a device
// whose buffer is full is reported busy rather than allowed to back up the hub.
//
// This registry is per process. Two instances have disjoint views of the fleet
// and neither can reach the other's devices; single instance is the supported
// topology until the Redis registry lands (docs/architecture.md §9).
package hub

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	v1 "github.com/KGMA74/relaix-server/gen/smsgateway/v1"
	"github.com/KGMA74/relaix-server/store"
)

// Errors returned by the hub. Callers match with errors.Is.
var (
	// ErrNotConnected means the device holds no stream on this instance. It is
	// not the same as "no such device": the device may be enrolled and simply
	// offline, or connected to another instance.
	ErrNotConnected = errors.New("hub: device not connected")

	// ErrDeviceBusy means the device's outbound buffer is full. The agent is
	// not reading fast enough, so the job was not delivered and the scheduler
	// should try another device rather than wait.
	ErrDeviceBusy = errors.New("hub: device busy")

	// ErrStopped means the hub's Run loop has exited.
	ErrStopped = errors.New("hub: stopped")
)

// Registration is the connection handle handed back to a stream handler.
type Registration struct {
	// ID distinguishes this connection from any earlier one for the same
	// device. A handler must quote it when unregistering, so that a slow
	// teardown of a replaced stream cannot evict the connection that took its
	// place.
	ID uuid.UUID

	// Out carries messages the hub wants sent to the device. The hub closes it
	// when the connection is evicted, unregistered, or the hub stops; a handler
	// ranges over it and ends the stream when it closes. Only the hub ever
	// sends on it, so only the hub may close it.
	Out <-chan *v1.ServerMessage
}

// DeviceState is a snapshot of what the hub knows about one connected device.
// It is a copy: callers may hold it without racing the hub goroutine.
type DeviceState struct {
	DeviceID       uuid.UUID
	RegistrationID uuid.UUID
	ConnectedAt    time.Time
	LastSeenAt     time.Time
	Health         *store.DeviceHealth
	// Busy is set once a send found the outbound buffer full. It clears on the
	// next heartbeat, which is itself evidence the agent is reading again.
	Busy bool
}

// Options configures a Hub. The zero value is usable; each field falls back to
// a documented default.
type Options struct {
	// HeartbeatTTL is how long a device stays ready without being heard from.
	// A device in a tunnel often leaves a socket that looks healthy for
	// minutes, so liveness is judged on heartbeats, not on the connection.
	// Default 90s, which tolerates two missed beats at the usual 30s cadence.
	HeartbeatTTL time.Duration

	// OutboundBuffer bounds each device's outbound queue. Small on purpose: a
	// deep buffer would hide a device that has stopped reading, and jobs
	// waiting in a per-device queue are jobs the scheduler could have given to
	// a healthy phone. Default 8.
	OutboundBuffer int

	// MinBattery is the level below which an unplugged device is not ready.
	// Sending on a dying battery risks the agent being killed mid-job.
	// Default 15.
	MinBattery int

	// Now is the clock, injectable so tests need not sleep. Defaults to
	// time.Now.
	Now func() time.Time

	Logger *slog.Logger
}

func (o *Options) withDefaults() {
	if o.HeartbeatTTL <= 0 {
		o.HeartbeatTTL = 90 * time.Second
	}
	if o.OutboundBuffer <= 0 {
		o.OutboundBuffer = 8
	}
	if o.MinBattery <= 0 {
		o.MinBattery = 15
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
}

// entry is the hub's private record of one connection. Only the Run goroutine
// touches it.
type entry struct {
	deviceID    uuid.UUID
	regID       uuid.UUID
	out         chan *v1.ServerMessage
	connectedAt time.Time
	lastSeenAt  time.Time
	health      *store.DeviceHealth
	busy        bool
}

func (e *entry) snapshot() DeviceState {
	s := DeviceState{
		DeviceID:       e.deviceID,
		RegistrationID: e.regID,
		ConnectedAt:    e.connectedAt,
		LastSeenAt:     e.lastSeenAt,
		Busy:           e.busy,
	}
	if e.health != nil {
		h := *e.health // copy, so the caller cannot mutate hub state
		s.Health = &h
	}
	return s
}

// Hub is the device registry. Create it with New and drive it with Run.
type Hub struct {
	opts    Options
	cmds    chan func(map[uuid.UUID]*entry)
	stopped chan struct{}
}

// New creates a Hub. It does nothing until Run is called.
func New(opts Options) *Hub {
	opts.withDefaults()
	return &Hub{
		opts: opts,
		// Unbuffered: a command is handed over only when the loop is ready for
		// it, so a backed-up hub applies backpressure to its callers instead of
		// hiding a growing queue.
		cmds:    make(chan func(map[uuid.UUID]*entry)),
		stopped: make(chan struct{}),
	}
}

// Run owns the registry until ctx is cancelled. It must be called exactly once,
// and everything else on Hub blocks until it is.
//
// On return every outbound channel is closed, which is how connected stream
// handlers learn to shut down.
func (h *Hub) Run(ctx context.Context) error {
	devices := make(map[uuid.UUID]*entry)

	defer func() {
		close(h.stopped)
		for _, e := range devices {
			close(e.out)
		}
		h.opts.Logger.Info("hub stopped", "connected", len(devices))
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case cmd := <-h.cmds:
			cmd(devices)
		}
	}
}

// do hands a closure to the Run goroutine and waits for it to finish.
//
// Commands are closures rather than a command struct per operation: the set is
// small, each one is a few lines, and the alternative is a type, a constructor
// and a dispatch arm for what amounts to one statement. The public methods
// below are the real API; this is the plumbing under them.
func (h *Hub) do(ctx context.Context, fn func(map[uuid.UUID]*entry)) error {
	done := make(chan struct{})
	wrapped := func(devices map[uuid.UUID]*entry) {
		defer close(done)
		fn(devices)
	}

	select {
	case h.cmds <- wrapped:
	case <-ctx.Done():
		return ctx.Err()
	case <-h.stopped:
		return ErrStopped
	}

	select {
	case <-done:
		return nil
	case <-h.stopped:
		return ErrStopped
	}
}

// Register adds a device and returns the handle its stream handler writes
// from. An existing connection for the same device is evicted: the newest
// stream wins, because a device that reconnected is by definition no longer
// reachable on the old one, and keeping both would let the scheduler hand work
// to a socket nobody is reading.
func (h *Hub) Register(ctx context.Context, deviceID uuid.UUID, health *store.DeviceHealth) (*Registration, error) {
	out := make(chan *v1.ServerMessage, h.opts.OutboundBuffer)
	now := h.opts.Now()
	reg := &Registration{ID: uuid.New(), Out: out}

	err := h.do(ctx, func(devices map[uuid.UUID]*entry) {
		if prev, ok := devices[deviceID]; ok {
			close(prev.out)
			h.opts.Logger.Info("hub evicting previous connection",
				"device_id", deviceID, "previous_registration", prev.regID)
		}
		devices[deviceID] = &entry{
			deviceID:    deviceID,
			regID:       reg.ID,
			out:         out,
			connectedAt: now,
			lastSeenAt:  now,
			health:      copyHealth(health),
		}
	})
	if err != nil {
		return nil, err
	}
	return reg, nil
}

// Unregister drops a device's connection, but only if regID still identifies
// the current one. A handler whose stream was replaced calls this on its way
// out, and without the check that late call would evict the connection that
// superseded it.
func (h *Hub) Unregister(ctx context.Context, deviceID, regID uuid.UUID, reason string) error {
	return h.do(ctx, func(devices map[uuid.UUID]*entry) {
		e, ok := devices[deviceID]
		if !ok || e.regID != regID {
			return
		}
		close(e.out)
		delete(devices, deviceID)
		h.opts.Logger.Info("hub unregistered device",
			"device_id", deviceID, "registration", regID, "reason", reason)
	})
}

// Heartbeat refreshes liveness and health. It also clears the busy flag: a
// device that is sending heartbeats again is reading its stream again.
func (h *Hub) Heartbeat(ctx context.Context, deviceID, regID uuid.UUID, health *store.DeviceHealth) error {
	now := h.opts.Now()
	var missing bool
	err := h.do(ctx, func(devices map[uuid.UUID]*entry) {
		e, ok := devices[deviceID]
		if !ok || e.regID != regID {
			missing = true
			return
		}
		e.lastSeenAt = now
		e.health = copyHealth(health)
		e.busy = false
	})
	if err != nil {
		return err
	}
	if missing {
		return ErrNotConnected
	}
	return nil
}

// SendJob queues a message for a device. It never blocks: if the outbound
// buffer is full the device is marked busy and ErrDeviceBusy is returned, so
// the scheduler can pick someone else on this tick instead of stalling every
// other hub operation behind one unresponsive phone.
func (h *Hub) SendJob(ctx context.Context, deviceID uuid.UUID, msg *v1.ServerMessage) error {
	var sendErr error
	err := h.do(ctx, func(devices map[uuid.UUID]*entry) {
		e, ok := devices[deviceID]
		if !ok {
			sendErr = ErrNotConnected
			return
		}
		select {
		case e.out <- msg:
		default:
			e.busy = true
			sendErr = ErrDeviceBusy
			h.opts.Logger.Warn("hub outbound buffer full",
				"device_id", deviceID, "buffer", h.opts.OutboundBuffer)
		}
	})
	if err != nil {
		return err
	}
	return sendErr
}

// Get returns a snapshot of one connected device.
func (h *Hub) Get(ctx context.Context, deviceID uuid.UUID) (DeviceState, error) {
	var (
		state DeviceState
		found bool
	)
	err := h.do(ctx, func(devices map[uuid.UUID]*entry) {
		e, ok := devices[deviceID]
		if !ok {
			return
		}
		state, found = e.snapshot(), true
	})
	if err != nil {
		return DeviceState{}, err
	}
	if !found {
		return DeviceState{}, ErrNotConnected
	}
	return state, nil
}

// ListConnected returns every connected device, ready or not, for the operator
// view behind GET /devices.
func (h *Hub) ListConnected(ctx context.Context) ([]DeviceState, error) {
	var out []DeviceState
	err := h.do(ctx, func(devices map[uuid.UUID]*entry) {
		out = make([]DeviceState, 0, len(devices))
		for _, e := range devices {
			out = append(out, e.snapshot())
		}
	})
	return out, err
}

// ListReady returns the devices eligible to receive work: connected, heard from
// within HeartbeatTTL, not busy, and reporting health that permits sending.
//
// Order is not defined. The scheduler decides how to spread load; the hub only
// says who is eligible.
func (h *Hub) ListReady(ctx context.Context) ([]DeviceState, error) {
	now := h.opts.Now()
	var out []DeviceState
	err := h.do(ctx, func(devices map[uuid.UUID]*entry) {
		out = make([]DeviceState, 0, len(devices))
		for _, e := range devices {
			if h.ready(e, now) {
				out = append(out, e.snapshot())
			}
		}
	})
	return out, err
}

// ready judges one entry. Called only from the Run goroutine.
func (h *Hub) ready(e *entry, now time.Time) bool {
	if e.busy {
		return false
	}
	if now.Sub(e.lastSeenAt) > h.opts.HeartbeatTTL {
		return false
	}
	// A connected device that has never reported health is not assumed fit:
	// until it says otherwise we do not know whether it has a SIM, permissions
	// or signal, and finding out by losing an SMS is the expensive way.
	if e.health == nil {
		return false
	}
	h2 := e.health
	switch {
	case !h2.SimReady:
		return false
	case !h2.PermissionsOK:
		return false
	case h2.SignalStrength <= 0:
		return false
	case !h2.IsCharging && h2.BatteryLevel < h.opts.MinBattery:
		return false
	}
	return true
}

func copyHealth(h *store.DeviceHealth) *store.DeviceHealth {
	if h == nil {
		return nil
	}
	c := *h
	return &c
}
