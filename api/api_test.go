package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/devices"
	"github.com/KGMA74/relaix-server/hub"
	"github.com/KGMA74/relaix-server/store"
	"github.com/KGMA74/relaix-server/store/storetest"
	"github.com/KGMA74/relaix-server/token"
)

var testNow = time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

const testAPIKey = "test-key"

// fakeHub answers with a fixed fleet.
type fakeHub struct {
	connected []hub.DeviceState
	ready     []hub.DeviceState
	err       error
}

func (f *fakeHub) ListConnected(context.Context) ([]hub.DeviceState, error) {
	return f.connected, f.err
}

func (f *fakeHub) ListReady(context.Context) ([]hub.DeviceState, error) {
	return f.ready, f.err
}

type harness struct {
	store    *storetest.Store
	hub      *fakeHub
	enroller *devices.Service
	handler  http.Handler
}

func newHarness(t *testing.T, opts Options) *harness {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := storetest.New()
	h := &fakeHub{}
	svc := devices.New(st, token.SHA256{}, devices.Options{
		TokenTTL: 15 * time.Minute,
		Now:      func() time.Time { return testNow },
		Logger:   logger,
	})

	if opts.Now == nil {
		opts.Now = func() time.Time { return testNow }
	}
	opts.Logger = logger

	srv := New(st, h, svc, opts)
	return &harness{store: st, hub: h, enroller: svc, handler: srv.Handler()}
}

// do sends a request and returns the recorder.
func (h *harness) do(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != nil {
		switch b := body.(type) {
		case string:
			reader = strings.NewReader(b)
		default:
			raw, err := json.Marshal(b)
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}
			reader = bytes.NewReader(raw)
		}
	}

	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testAPIKey)

	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return v
}

func readyDevice() (uuid.UUID, hub.DeviceState) {
	id := uuid.New()
	return id, hub.DeviceState{
		DeviceID:   id,
		LastSeenAt: testNow,
		Health: &store.DeviceHealth{
			BatteryLevel: 80, SignalStrength: 4, SimReady: true,
			PermissionsOK: true, NetworkType: "LTE", ReportedAt: testNow,
		},
	}
}

// ---------------------------------------------------------------------------
// auth
// ---------------------------------------------------------------------------

func TestAuthRejectsMissingAndWrongKey(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})

	tests := []struct {
		name   string
		header string
	}{
		{"no header", ""},
		{"wrong scheme", "Basic " + testAPIKey},
		{"wrong key", "Bearer nope"},
		{"empty bearer", "Bearer "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/devices", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
		})
	}
}

func TestAuthDisabledAllowsEverything(t *testing.T) {
	h := newHarness(t, Options{}) // no APIKey

	req := httptest.NewRequest(http.MethodGet, "/devices", nil)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// POST /send
// ---------------------------------------------------------------------------

func TestSendCreatesQueuedJob(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})

	rec := h.do(t, http.MethodPost, "/send", sendRequest{
		Recipient: "+33600000000",
		Body:      "hello",
		Priority:  5,
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
	}

	got := decode[jobResponse](t, rec)
	if got.Status != string(store.JobPending) {
		t.Errorf("status = %q, want %q", got.Status, store.JobPending)
	}
	// Queued by default: eventual delivery is the safer assumption.
	if got.Mode != string(store.ModeQueued) {
		t.Errorf("mode = %q, want %q", got.Mode, store.ModeQueued)
	}
	if got.Priority != 5 {
		t.Errorf("priority = %d, want 5", got.Priority)
	}

	id := uuid.MustParse(got.JobID)
	if stored := h.store.JobByID(id); stored == nil {
		t.Fatal("job was not persisted")
	}
}

// Immediate must fail synchronously: for an OTP, a prompt error is worth more
// than a late message, because only a prompt error allows a fallback.
func TestImmediateSendFailsFastWithNoReadyDevice(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})

	rec := h.do(t, http.MethodPost, "/send", sendRequest{
		Recipient: "+33600000000",
		Body:      "otp",
		Mode:      string(store.ModeImmediate),
	})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body)
	}

	got := decode[errorBody](t, rec)
	if got.Error.Code != "no_device_available" {
		t.Errorf("code = %q", got.Error.Code)
	}

	// And nothing was persisted: failing fast means no job to chase later.
	devices, _ := h.store.Devices().List(context.Background())
	_ = devices
	if events := h.store.AllEvents(); len(events) != 0 {
		t.Errorf("recorded %d events for a rejected send, want 0", len(events))
	}
}

func TestImmediateSendSucceedsWithAReadyDevice(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	_, dev := readyDevice()
	h.hub.ready = []hub.DeviceState{dev}

	rec := h.do(t, http.MethodPost, "/send", sendRequest{
		Recipient: "+33600000000",
		Body:      "otp",
		Mode:      string(store.ModeImmediate),
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
	}
}

// A caller naming a device means a specific SIM; another ready device is not a
// substitute.
func TestImmediateSendChecksTheRequestedDevice(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	_, other := readyDevice()
	h.hub.ready = []hub.DeviceState{other}

	absent := uuid.New().String()
	rec := h.do(t, http.MethodPost, "/send", sendRequest{
		Recipient: "+33600000000",
		Body:      "otp",
		Mode:      string(store.ModeImmediate),
		DeviceID:  &absent,
	})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body)
	}
}

// A queued job must be accepted with an empty fleet: that is the difference
// between the two modes.
func TestQueuedSendIsAcceptedWithNoDevices(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})

	rec := h.do(t, http.MethodPost, "/send", sendRequest{
		Recipient: "+33600000000",
		Body:      "bulk",
		Mode:      string(store.ModeQueued),
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
	}
}

func TestSendValidation(t *testing.T) {
	future := testNow.Add(time.Hour).Format(time.RFC3339)
	past := testNow.Add(-time.Hour).Format(time.RFC3339)
	bad := "not-a-uuid"

	tests := []struct {
		name string
		req  sendRequest
		code string
	}{
		{"no recipient", sendRequest{Body: "x"}, "missing_recipient"},
		{"recipient without plus", sendRequest{Recipient: "33600000000", Body: "x"}, "invalid_recipient"},
		{"recipient with letters", sendRequest{Recipient: "+336000abc00", Body: "x"}, "invalid_recipient"},
		{"recipient too short", sendRequest{Recipient: "+3360", Body: "x"}, "invalid_recipient"},
		{"leading zero", sendRequest{Recipient: "+0336000000", Body: "x"}, "invalid_recipient"},
		{"no body", sendRequest{Recipient: "+33600000000"}, "missing_body"},
		{"body too long", sendRequest{Recipient: "+33600000000", Body: strings.Repeat("a", maxBodyRunes+1)}, "body_too_long"},
		{"bad mode", sendRequest{Recipient: "+33600000000", Body: "x", Mode: "express"}, "invalid_mode"},
		{"bad device id", sendRequest{Recipient: "+33600000000", Body: "x", DeviceID: &bad}, "invalid_device_id"},
		{"expires in the past", sendRequest{Recipient: "+33600000000", Body: "x", ExpiresAt: &past}, "expires_in_past"},
		{"bad callback url", sendRequest{Recipient: "+33600000000", Body: "x", CallbackURL: "ftp://x/y"}, "invalid_callback_url"},
		{"relative callback url", sendRequest{Recipient: "+33600000000", Body: "x", CallbackURL: "/hook"}, "invalid_callback_url"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, Options{APIKey: testAPIKey})
			rec := h.do(t, http.MethodPost, "/send", tc.req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
			}
			got := decode[errorBody](t, rec)
			if got.Error.Code != tc.code {
				t.Errorf("code = %q, want %q", got.Error.Code, tc.code)
			}
		})
	}

	// A job that expires before it may be sent could never go out.
	t.Run("expires before scheduled", func(t *testing.T) {
		h := newHarness(t, Options{APIKey: testAPIKey})
		soon := testNow.Add(30 * time.Minute).Format(time.RFC3339)
		rec := h.do(t, http.MethodPost, "/send", sendRequest{
			Recipient: "+33600000000", Body: "x",
			ScheduledAt: &future, ExpiresAt: &soon,
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
		}
		if got := decode[errorBody](t, rec); got.Error.Code != "expires_before_scheduled" {
			t.Errorf("code = %q", got.Error.Code)
		}
	})
}

// A misspelled field is a caller bug, and silently ignoring it means they find
// out from a message that went at the wrong time.
func TestUnknownFieldsAreRejected(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	rec := h.do(t, http.MethodPost, "/send",
		`{"recipient":"+33600000000","body":"x","schduledAt":"2026-07-30T13:00:00Z"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
	if got := decode[errorBody](t, rec); got.Error.Code != "invalid_json" {
		t.Errorf("code = %q", got.Error.Code)
	}
}

func TestScheduledSendIsStored(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	at := testNow.Add(2 * time.Hour).Format(time.RFC3339)

	rec := h.do(t, http.MethodPost, "/send", sendRequest{
		Recipient: "+33600000000", Body: "later", ScheduledAt: &at,
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
	}
	got := decode[jobResponse](t, rec)
	if got.ScheduledAt == nil || *got.ScheduledAt != at {
		t.Errorf("scheduledAt = %v, want %q", got.ScheduledAt, at)
	}
}

// An immediate job scheduled for later cannot be checked against the fleet
// now, so it must not be rejected now.
func TestImmediateWithScheduledAtIsNotCheckedAgainstTheFleet(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	at := testNow.Add(2 * time.Hour).Format(time.RFC3339)

	rec := h.do(t, http.MethodPost, "/send", sendRequest{
		Recipient: "+33600000000", Body: "later",
		Mode: string(store.ModeImmediate), ScheduledAt: &at,
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
	}
}

func TestBodySizeIsBounded(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey, MaxBodyBytes: 128})
	rec := h.do(t, http.MethodPost, "/send", sendRequest{
		Recipient: "+33600000000",
		Body:      strings.Repeat("a", 500),
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

// ---------------------------------------------------------------------------
// GET /jobs/{id} and DELETE /jobs/{id}
// ---------------------------------------------------------------------------

func TestGetJob(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	job := h.store.SeedJob(&store.Job{
		Recipient: "+33600000000", Body: "x", Mode: store.ModeQueued,
		Status: store.JobSent, PartsSent: 2,
	})

	rec := h.do(t, http.MethodGet, "/jobs/"+job.ID.String(), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[jobResponse](t, rec)
	if got.JobID != job.ID.String() || got.Status != string(store.JobSent) || got.PartsSent != 2 {
		t.Errorf("job = %+v", got)
	}
}

func TestGetJobNotFoundAndBadID(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})

	if rec := h.do(t, http.MethodGet, "/jobs/"+uuid.NewString(), nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown job status = %d, want 404", rec.Code)
	}
	if rec := h.do(t, http.MethodGet, "/jobs/not-a-uuid", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad id status = %d, want 400", rec.Code)
	}
}

func TestCancelJob(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	job := h.store.SeedJob(&store.Job{
		Recipient: "+33600000000", Body: "x", Mode: store.ModeQueued,
	})

	rec := h.do(t, http.MethodDelete, "/jobs/"+job.ID.String(), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := h.store.JobByID(job.ID); got.Status != store.JobCancelled {
		t.Errorf("status = %q, want %q", got.Status, store.JobCancelled)
	}
}

// There is no recalling a message the handset already passed to the network.
func TestCancelAlreadySentJobIsAConflict(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	job := h.store.SeedJob(&store.Job{
		Recipient: "+33600000000", Body: "x", Mode: store.ModeQueued,
		Status: store.JobSent,
	})

	rec := h.do(t, http.MethodDelete, "/jobs/"+job.ID.String(), nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body)
	}
	if got := decode[errorBody](t, rec); got.Error.Code != "already_final" {
		t.Errorf("code = %q", got.Error.Code)
	}
}

func TestCancelUnknownJob(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	rec := h.do(t, http.MethodDelete, "/jobs/"+uuid.NewString(), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// GET /devices
// ---------------------------------------------------------------------------

func TestListDevicesMergesStoreAndHub(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	ctx := context.Background()

	online, err := h.store.Devices().Create(ctx, &store.Device{Label: "online"}, "hash-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := h.store.Devices().Create(ctx, &store.Device{Label: "offline"}, "hash-2"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	state := hub.DeviceState{
		DeviceID:   online.ID,
		LastSeenAt: testNow,
		Health: &store.DeviceHealth{
			BatteryLevel: 42, SignalStrength: 3, SimReady: true,
			PermissionsOK: true, ReportedAt: testNow,
		},
	}
	h.hub.connected = []hub.DeviceState{state}
	h.hub.ready = []hub.DeviceState{state}

	rec := h.do(t, http.MethodGet, "/devices", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}

	body := decode[struct {
		Devices []deviceResponse `json:"devices"`
	}](t, rec)
	if len(body.Devices) != 2 {
		t.Fatalf("listed %d devices, want 2", len(body.Devices))
	}

	byID := map[string]deviceResponse{}
	for _, d := range body.Devices {
		byID[d.DeviceID] = d
	}

	got := byID[online.ID.String()]
	if !got.Connected || !got.Ready {
		t.Errorf("connected device reported connected=%v ready=%v", got.Connected, got.Ready)
	}
	if got.Health == nil || got.Health.BatteryLevel != 42 {
		t.Errorf("live health missing: %+v", got.Health)
	}

	for id, d := range byID {
		if id != online.ID.String() && (d.Connected || d.Ready) {
			t.Errorf("offline device reported connected=%v ready=%v", d.Connected, d.Ready)
		}
	}
}

func TestListDevicesEmptyFleet(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	rec := h.do(t, http.MethodGet, "/devices", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// An empty list, not null: a caller iterating the field should not have to
	// special-case an empty fleet.
	if !strings.Contains(rec.Body.String(), `"devices":[]`) {
		t.Errorf("body = %s, want an empty array", rec.Body)
	}
}

func TestListDevicesPropagatesHubFailure(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	h.hub.err = errors.New("hub is down")

	rec := h.do(t, http.MethodGet, "/devices", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// POST /admin/devices/enroll-token
// ---------------------------------------------------------------------------

func TestEnrollTokenReturnsTokenAndQR(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey, PublicURL: "grpc://relaix.example:9090"})

	rec := h.do(t, http.MethodPost, "/admin/devices/enroll-token", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}

	got := decode[enrollTokenResponse](t, rec)
	if got.Token == "" {
		t.Fatal("no token returned")
	}
	if got.ExpiresAt != testNow.Add(15*time.Minute).UTC().Format(time.RFC3339) {
		t.Errorf("expiresAt = %q", got.ExpiresAt)
	}

	// The QR must carry the endpoint alongside the token, so the phone needs no
	// separate configuration step.
	var payload enrollPayload
	if err := json.Unmarshal([]byte(got.Payload), &payload); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	if payload.Endpoint != "grpc://relaix.example:9090" || payload.Token != got.Token {
		t.Errorf("payload = %+v", payload)
	}

	png, err := base64.StdEncoding.DecodeString(got.QRCodePNG)
	if err != nil {
		t.Fatalf("qr is not base64: %v", err)
	}
	if !bytes.HasPrefix(png, []byte("\x89PNG\r\n\x1a\n")) {
		t.Error("qr payload is not a PNG")
	}

	// The token must not leak through a cache.
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", rec.Header().Get("Cache-Control"))
	}
}

// The minted token must actually enroll a device — the endpoint is only useful
// if what it hands out works.
func TestMintedTokenEnrollsADevice(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})

	rec := h.do(t, http.MethodPost, "/admin/devices/enroll-token", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	got := decode[enrollTokenResponse](t, rec)

	enrolled, err := h.enroller.Enroll(context.Background(), got.Token, &store.Device{Label: "phone"})
	if err != nil {
		t.Fatalf("the minted token did not enroll: %v", err)
	}
	if enrolled.DeviceToken == "" {
		t.Error("enrollment returned no device token")
	}
}

func TestEnrollTokenAsPNG(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})

	req := httptest.NewRequest(http.MethodPost, "/admin/devices/enroll-token?format=png", nil)
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	if !bytes.HasPrefix(rec.Body.Bytes(), []byte("\x89PNG\r\n\x1a\n")) {
		t.Error("body is not a PNG")
	}
}

// ---------------------------------------------------------------------------
// routing and middleware
// ---------------------------------------------------------------------------

func TestMethodAndRouteMismatches(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})

	tests := []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/send", http.StatusMethodNotAllowed},
		{http.MethodPost, "/devices", http.StatusMethodNotAllowed},
		{http.MethodGet, "/nope", http.StatusNotFound},
	}
	for _, tc := range tests {
		rec := h.do(t, tc.method, tc.path, nil)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, rec.Code, tc.want)
		}
	}
}

func TestRequestIDIsEchoed(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	rec := h.do(t, http.MethodGet, "/devices", nil)

	if rec.Header().Get("X-Request-Id") == "" {
		t.Error("no X-Request-Id returned")
	}
}
