package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/store"
)

func TestUSSDCreatesAJobForItsDevice(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	dev := h.store.SeedDevice(&store.Device{Enabled: true, Capabilities: []string{store.CapUSSD}}).ID.String()

	rec := h.do(t, http.MethodPost, "/ussd", ussdRequest{
		DeviceID: &dev, Code: " *144# ", Replies: []string{"2", " 1 "}, SubscriptionID: new(int32(2)),
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
	}
	got := decode[jobResponse](t, rec)
	if got.Kind != "ussd" || got.Body != "*144#" || got.Status != string(store.JobPending) {
		t.Errorf("response = %+v", got)
	}
	if len(got.Replies) != 2 || got.Replies[1] != "1" {
		t.Errorf("replies = %q, want trimmed [2 1]", got.Replies)
	}

	stored := h.store.JobByID(uuid.MustParse(got.JobID))
	if stored.Kind != store.KindUSSD || stored.RequestedDeviceID == nil || stored.RequestedDeviceID.String() != dev {
		t.Errorf("stored = %+v", stored)
	}
	if stored.SubscriptionID == nil || *stored.SubscriptionID != 2 {
		t.Errorf("subscription = %v, want 2", stored.SubscriptionID)
	}
}

func TestUSSDHidesASensitiveCode(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	dev := h.store.SeedDevice(&store.Device{Enabled: true}).ID.String()

	rec := h.do(t, http.MethodPost, "/ussd", ussdRequest{
		DeviceID: &dev, Code: "*144*4*1234#", Replies: []string{"1234"}, Sensitive: true,
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "1234") {
		t.Errorf("the response shows the secret: %s", rec.Body)
	}
	got := decode[jobResponse](t, rec)
	if !got.Sensitive {
		t.Error("sensitive not reported")
	}

	listed := h.do(t, http.MethodGet, "/jobs?kind=ussd", nil)
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), "1234") {
		t.Errorf("listing = %d %s", listed.Code, listed.Body)
	}
	if !strings.Contains(listed.Body.String(), got.JobID) {
		t.Errorf("kind=ussd does not list the job: %s", listed.Body)
	}
	if sms := h.do(t, http.MethodGet, "/jobs?kind=sms", nil); strings.Contains(sms.Body.String(), got.JobID) {
		t.Errorf("kind=sms lists the USSD job: %s", sms.Body)
	}
}

func TestUSSDValidation(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	dev := uuid.NewString()
	tooMany := make([]string, maxReplies+1)
	for i := range tooMany {
		tooMany[i] = "1"
	}

	cases := []struct {
		name string
		req  ussdRequest
		code string
	}{
		{"no device", ussdRequest{Code: "*144#"}, "missing_device"},
		{"no code", ussdRequest{DeviceID: &dev}, "missing_code"},
		{"letters", ussdRequest{DeviceID: &dev, Code: "*144a#"}, "invalid_code"},
		{"no closing hash", ussdRequest{DeviceID: &dev, Code: "*144"}, "invalid_code"},
		{"not a service code", ussdRequest{DeviceID: &dev, Code: "144#"}, "invalid_code"},
		{"too long", ussdRequest{DeviceID: &dev, Code: "*" + strings.Repeat("1", maxUSSDRunes) + "#"}, "invalid_code"},
		{"empty reply", ussdRequest{DeviceID: &dev, Code: "*144#", Replies: []string{" "}}, "invalid_reply"},
		{"control character", ussdRequest{DeviceID: &dev, Code: "*144#", Replies: []string{"1\n2"}}, "invalid_reply"},
		{"too many replies", ussdRequest{DeviceID: &dev, Code: "*144#", Replies: tooMany}, "too_many_replies"},
		{"bad mode", ussdRequest{DeviceID: &dev, Code: "*144#", Mode: "express"}, "invalid_mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do(t, http.MethodPost, "/ussd", tc.req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
			}
			if got := decode[errorBody](t, rec); got.Error.Code != tc.code {
				t.Errorf("code = %q, want %q", got.Error.Code, tc.code)
			}
		})
	}
}

func TestUSSDOnAnotherAccountsDeviceIsUnknown(t *testing.T) {
	h := newHarness(t, Options{Authenticator: AuthenticatorFunc(func(*http.Request) (Principal, error) {
		return Principal{AccountID: uuid.New()}, nil
	})})
	dev := h.store.SeedDevice(&store.Device{Enabled: true}).ID.String()

	rec := h.do(t, http.MethodPost, "/ussd", ussdRequest{DeviceID: &dev, Code: "*144#"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
	if got := decode[errorBody](t, rec); got.Error.Code != "unknown_device" {
		t.Errorf("code = %q, want unknown_device", got.Error.Code)
	}
}

func TestUSSDAcceptsAPinPlaceholder(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	dev := h.store.SeedDevice(&store.Device{Enabled: true}).ID.String()
	rec := h.do(t, http.MethodPost, "/ussd", ussdRequest{DeviceID: &dev, Code: "*144*2*1*70000000*5000*{pin}#"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if got := decode[jobResponse](t, rec); got.Body != "*144*2*1*70000000*5000*{pin}#" {
		t.Errorf("body = %q: the placeholder is no secret and stays visible", got.Body)
	}
	// The PIN a menu asks for on its last screen: a reply like any other,
	// filled in on the phone.
	menu := h.do(t, http.MethodPost, "/ussd", ussdRequest{DeviceID: &dev, Code: "*144#", Replies: []string{"2", "1", "{pin}"}})
	if menu.Code != http.StatusAccepted {
		t.Fatalf("menu status = %d: %s", menu.Code, menu.Body)
	}
	if got := decode[jobResponse](t, menu); len(got.Replies) != 3 || got.Replies[2] != "{pin}" {
		t.Errorf("replies = %q, want the placeholder kept for the phone", got.Replies)
	}
	bad := h.do(t, http.MethodPost, "/ussd", ussdRequest{DeviceID: &dev, Code: "*144*{code}#"})
	if bad.Code != http.StatusBadRequest {
		t.Errorf("another placeholder = %d, want 400", bad.Code)
	}
}
