// Package events fans state changes out to whoever is watching — today, the
// dashboard's live stream (GET /events).
//
// An event is a notification, not the state itself: it says "device X
// changed" and the dashboard re-reads device X through the API it already
// uses. That keeps the stream small, keeps one source of truth, and means a
// missed event costs a refresh rather than a wrong screen.
//
// Delivery is best-effort and never blocks the publisher. The gRPC handlers
// and the scheduler publish from their hot paths, and a slow browser tab must
// not be able to stall a phone's heartbeat. A subscriber that falls behind is
// dropped instead — its channel is closed — and a client that reconnects
// starts by re-reading everything, so nothing is silently lost.
package events

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// Types of event. Dotted by subject so a client can route on the prefix.
const (
	DeviceEnrolled     = "device.enrolled"
	DeviceConnected    = "device.connected"
	DeviceDisconnected = "device.disconnected"
	DeviceHealth       = "device.health"
	DeviceUpdated      = "device.updated"
	DeviceRemoved      = "device.removed"
	JobCreated         = "job.created"
	JobUpdated         = "job.updated"
	SmsReceived        = "sms.received"
	AutomationRun      = "automation.run"
)

// Event is one change. Only the fields that name what changed are set.
type Event struct {
	Type     string `json:"type"`
	DeviceID string `json:"deviceId,omitempty"`
	JobID    string `json:"jobId,omitempty"`
	// MessageID names a received SMS (SmsReceived).
	MessageID string    `json:"messageId,omitempty"`
	Status    string    `json:"status,omitempty"`
	At        time.Time `json:"at"`

	// Reenrolled marks a DeviceEnrolled that took back an existing device
	// rather than creating one.
	Reenrolled bool `json:"reenrolled,omitempty"`

	// AccountID owns what the event is about: the job's account for a job
	// event, the device's for a device event. Streams scoped to one account
	// only carry its events, and one with no account reaches only streams that
	// see every account. Not sent: subscribers already know whose stream it is.
	AccountID uuid.UUID `json:"-"`
}

// subscriberBuffer absorbs a burst — a fanout of a hundred jobs is a hundred
// job.created at once — without dropping a client that is merely busy.
const subscriberBuffer = 256

// Broker is an in-process publish/subscribe hub. The zero value is not usable;
// call New. A nil *Broker is, though: every method is a no-op on it, so a
// component built without one (most tests) needs no special case.
type Broker struct {
	mu     sync.Mutex
	subs   map[chan Event]struct{}
	closed bool
	now    func() time.Time
}

// New creates a Broker.
func New() *Broker {
	return &Broker{subs: make(map[chan Event]struct{}), now: time.Now}
}

// Publish delivers e to every subscriber that has room for it. At is stamped
// here when the caller left it zero.
func (b *Broker) Publish(e Event) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	if e.At.IsZero() {
		e.At = b.now().UTC()
	}
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
			// Full: this subscriber is not keeping up. Dropping one event
			// would leave its screen quietly wrong; closing tells it to
			// reconnect and resync.
			delete(b.subs, ch)
			close(ch)
		}
	}
}

// Subscribe returns a channel of events and a function to stop receiving
// them. The channel is closed when the subscriber falls behind, when cancel is
// called, or when the broker closes.
func (b *Broker) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, subscriberBuffer)
	if b == nil {
		close(ch)
		return ch, func() {}
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	b.subs[ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if _, ok := b.subs[ch]; ok {
				delete(b.subs, ch)
				close(ch)
			}
		})
	}
	return ch, cancel
}

// Close ends every subscription and refuses new ones. Called on shutdown: an
// open event stream is a request that never finishes on its own, and
// http.Server.Shutdown would otherwise wait on it until its deadline.
func (b *Broker) Close() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for ch := range b.subs {
		close(ch)
	}
	b.subs = nil
}
