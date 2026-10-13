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
	var got InboundPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !Verify(secret, r.Header.Get(HeaderTimestamp), body, r.Header.Get(HeaderSignature)) {
			t.Error("bad signature")
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.Unmarshal(body, &got)
	}))
	defer srv.Close()

	st := storetest.New()
	dev := st.SeedDevice(&store.Device{Enabled: true}).ID
	now := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	m, _, err := st.Inbound().Record(ctx, &store.InboundMessage{
		DeviceID: &dev, DeviceRef: "r", Sender: "OrangeMoney", Body: "Vous avez recu 5000 FCFA", ReceivedAt: now,
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
	if got.Type != "sms.received" || got.Sender != "OrangeMoney" || got.MessageID != m.ID.String() || got.Attempt != 2 {
		t.Errorf("payload = %+v", got)
	}
}
