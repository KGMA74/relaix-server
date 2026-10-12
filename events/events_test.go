package events

import (
	"sync"
	"testing"
	"time"
)

func TestPublishReachesEverySubscriber(t *testing.T) {
	b := New()
	a, cancelA := b.Subscribe()
	defer cancelA()
	c, cancelC := b.Subscribe()
	defer cancelC()

	b.Publish(Event{Type: DeviceConnected, DeviceID: "d1"})

	for _, ch := range []<-chan Event{a, c} {
		select {
		case e := <-ch:
			if e.Type != DeviceConnected || e.DeviceID != "d1" {
				t.Fatalf("got %+v", e)
			}
			if e.At.IsZero() {
				t.Fatal("At not stamped")
			}
		case <-time.After(time.Second):
			t.Fatal("event not delivered")
		}
	}
}

func TestCancelStopsDelivery(t *testing.T) {
	b := New()
	ch, cancel := b.Subscribe()
	cancel()
	cancel() // idempotent

	b.Publish(Event{Type: JobCreated})
	if _, ok := <-ch; ok {
		t.Fatal("cancelled subscription still received an event")
	}
}

// A subscriber that stops reading must not block the publisher; it is
// dropped, and learns so from its channel closing.
func TestSlowSubscriberIsDroppedNotWaitedOn(t *testing.T) {
	b := New()
	slow, cancel := b.Subscribe()
	defer cancel()

	done := make(chan struct{})
	go func() {
		for range subscriberBuffer + 10 {
			b.Publish(Event{Type: JobUpdated})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a subscriber that does not read")
	}

	n := 0
	for range slow {
		n++
	}
	if n != subscriberBuffer {
		t.Fatalf("drained %d events before close, want the %d buffered", n, subscriberBuffer)
	}
}

func TestCloseEndsSubscriptionsAndRefusesNewOnes(t *testing.T) {
	b := New()
	ch, cancel := b.Subscribe()
	defer cancel()

	b.Close()
	if _, ok := <-ch; ok {
		t.Fatal("subscription open after Close")
	}
	late, lateCancel := b.Subscribe()
	defer lateCancel()
	if _, ok := <-late; ok {
		t.Fatal("Subscribe after Close returned an open channel")
	}
	b.Publish(Event{Type: JobCreated}) // must not panic on a closed broker
}

func TestNilBrokerIsANoOp(t *testing.T) {
	var b *Broker
	b.Publish(Event{Type: JobCreated})
	ch, cancel := b.Subscribe()
	cancel()
	if _, ok := <-ch; ok {
		t.Fatal("nil broker returned an open channel")
	}
	b.Close()
}

func TestConcurrentUse(t *testing.T) {
	b := New()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			ch, cancel := b.Subscribe()
			go func() {
				for range ch {
				}
			}()
			time.Sleep(time.Millisecond)
			cancel()
		}()
		go func() {
			defer wg.Done()
			for range 100 {
				b.Publish(Event{Type: DeviceHealth})
			}
		}()
	}
	wg.Wait()
	b.Close()
}
