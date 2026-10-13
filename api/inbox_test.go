package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/store"
)

func TestInboxListsAndFiltersReceivedSMS(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	ctx := context.Background()
	dev := h.store.SeedDevice(&store.Device{Enabled: true}).ID
	base := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	for i, sender := range []string{"OrangeMoney", "MoovMoney", "OrangeMoney"} {
		if _, _, err := h.store.Inbound().Record(ctx, &store.InboundMessage{
			DeviceID: &dev, DeviceRef: uuid.NewString(), Sender: sender, Body: "x",
			ReceivedAt: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	rec := h.do(t, http.MethodGet, "/inbox?sender=orangemoney&limit=1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	page := decode[listInboxResponse](t, rec)
	if len(page.Messages) != 1 || page.NextCursor == "" || page.Messages[0].ReceivedAt != "2026-10-01T08:02:00Z" {
		t.Fatalf("first page = %+v", page)
	}
	next := decode[listInboxResponse](t, h.do(t, http.MethodGet, "/inbox?sender=orangemoney&limit=1&cursor="+page.NextCursor, nil))
	if len(next.Messages) != 1 || next.NextCursor != "" || next.Messages[0].ReceivedAt != "2026-10-01T08:00:00Z" {
		t.Fatalf("second page = %+v", next)
	}

	one := h.do(t, http.MethodGet, "/inbox/"+page.Messages[0].MessageID, nil)
	if one.Code != http.StatusOK || decode[inboundResponse](t, one).Sender != "OrangeMoney" {
		t.Errorf("GET /inbox/{id} = %d %s", one.Code, one.Body)
	}
	if missing := h.do(t, http.MethodGet, "/inbox/"+uuid.NewString(), nil); missing.Code != http.StatusNotFound {
		t.Errorf("unknown message = %d, want 404", missing.Code)
	}
	if bad := h.do(t, http.MethodGet, "/inbox?from=yesterday", nil); bad.Code != http.StatusBadRequest {
		t.Errorf("bad from = %d, want 400", bad.Code)
	}
}

func TestInboxIsScopedToTheAccount(t *testing.T) {
	other := uuid.New()
	h := newHarness(t, Options{Authenticator: AuthenticatorFunc(func(*http.Request) (Principal, error) {
		return Principal{AccountID: other}, nil
	})})
	dev := h.store.SeedDevice(&store.Device{Enabled: true}).ID
	m, _, err := h.store.Inbound().Record(context.Background(), &store.InboundMessage{
		DeviceID: &dev, DeviceRef: "r", Sender: "OrangeMoney", Body: "x", ReceivedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if list := decode[listInboxResponse](t, h.do(t, http.MethodGet, "/inbox", nil)); len(list.Messages) != 0 {
		t.Errorf("another account sees %d messages", len(list.Messages))
	}
	if one := h.do(t, http.MethodGet, "/inbox/"+m.ID.String(), nil); one.Code != http.StatusNotFound {
		t.Errorf("another account's message = %d, want 404", one.Code)
	}
}
