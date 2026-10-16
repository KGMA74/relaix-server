package callback

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KGMA74/relaix-server/store"
	"github.com/KGMA74/relaix-server/store/storetest"
)

func TestInboundWebhookIsSignedRetriedAndDelivered(t *testing.T) {
	ctx := context.Background()
	secret := []byte("s3cret")
	var calls atomic.Int32
	var got struct {
		Event string         `json:"event"`
		Data  InboundPayload `json:"data"`
	}
	var raw []byte
	var deliveries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		deliveries = append(deliveries, r.Header.Get(HeaderDelivery))
		if !Verify(secret, r.Header.Get(HeaderTimestamp), body, r.Header.Get(HeaderSignature)) {
			t.Error("bad signature")
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		raw = body
		_ = json.Unmarshal(body, &got)
	}))
	defer srv.Close()

	st := storetest.New()
	dev := st.SeedDevice(&store.Device{Enabled: true}).ID
	now := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	m, _, err := st.Inbound().Record(ctx, &store.InboundMessage{
		DeviceID: &dev, DeviceRef: "r", Sender: "OrangeMoney", Body: "Vous avez recu 5 000 FCFA de 70123456", ReceivedAt: now,
		AutomationID: "a1", Fields: map[string]string{"amount": "5 000", "from": "070123456", "sim": "1"},
		Webhook: store.CallbackState{URL: srv.URL, NextAttemptAt: &now},
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	clock := now
	opts := WatcherOptions{Now: func() time.Time { return clock }, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Jitter: 0}
	notifier := NewNotifier(Options{Secret: secret, Now: opts.Now})
	w := NewInboundWatcher(st, notifier, opts)

	if err := w.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	after, _ := st.Inbound().Get(ctx, m.ID)
	if after.Webhook.DeliveredAt != nil || after.Webhook.Attempts != 1 || after.Webhook.NextAttemptAt == nil {
		t.Fatalf("after a 503: %+v", after.Webhook)
	}

	clock = *after.Webhook.NextAttemptAt
	if err := w.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	done, _ := st.Inbound().Get(ctx, m.ID)
	if done.Webhook.DeliveredAt == nil {
		t.Fatalf("not delivered: %+v", done.Webhook)
	}
	if got.Event != "sms.received" || got.Data.Sender != "OrangeMoney" || got.Data.MessageID != m.ID.String() || got.Data.Attempt != 2 {
		t.Errorf("payload = %+v", got)
	}
	// One id for both attempts: the receiver dedupes on it.
	if len(deliveries) != 2 || deliveries[0] != m.ID.String() || deliveries[1] != deliveries[0] {
		t.Errorf("delivery headers = %q, want %s twice", deliveries, m.ID)
	}
	// The amount as a number; a number with a leading zero, and the sim, stay
	// text only.
	var typed struct {
		Data struct {
			Numbers map[string]any `json:"numbers"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &typed)
	if len(typed.Data.Numbers) != 1 || typed.Data.Numbers["amount"] != float64(5000) {
		t.Errorf("numbers = %v, want {amount: 5000}", typed.Data.Numbers)
	}
}
