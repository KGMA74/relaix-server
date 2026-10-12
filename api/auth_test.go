package api

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/events"
	"github.com/KGMA74/relaix-server/hub"
	"github.com/KGMA74/relaix-server/store"
	"github.com/KGMA74/relaix-server/token"
)

// tenants is a gateway shared by two accounts, each with its own key, plus
// the operator's key that sees both.
type tenants struct {
	*harness
	a, b uuid.UUID
}

const (
	keyA = "key-a"
	keyB = "key-b"
)

func newTenants(t *testing.T, opts Options) *tenants {
	t.Helper()
	st := &tenants{}
	opts.Authenticator = AuthenticatorFunc(func(r *http.Request) (Principal, error) {
		tok, err := BearerToken(r)
		if err != nil {
			return Principal{}, err
		}
		switch tok {
		case testAPIKey:
			return operator, nil
		case keyA:
			return Principal{AccountID: st.a}, nil
		case keyB:
			return Principal{AccountID: st.b}, nil
		}
		return Principal{}, errors.New("invalid api key")
	})
	st.harness = newHarness(t, opts)

	ctx := context.Background()
	for _, dst := range []*uuid.UUID{&st.a, &st.b} {
		acc, err := st.store.Accounts().Create(ctx, "tenant")
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		*dst = acc.ID
	}
	return st
}

// as sends a bodiless request with the given key.
func (h *harness) as(t *testing.T, key, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	return h.withBody(t, key, method, path, "")
}

// readyState is a ready, healthy connection for id.
func readyState(id uuid.UUID) hub.DeviceState {
	_, st := readyDevice()
	st.DeviceID = id
	return st
}

// device stores a device owned by account.
func (ts *tenants) device(t *testing.T, account uuid.UUID) *store.Device {
	t.Helper()
	d, err := ts.store.Devices().Create(context.Background(), &store.Device{
		AccountID: account, Label: "phone", Enabled: true, CreatedAt: testNow,
	}, uuid.NewString())
	if err != nil {
		t.Fatalf("create device: %v", err)
	}
	return d
}

func TestSharedKeyIsTheOperator(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	if _, err := SharedKey("k").Authenticate(r); err == nil || err.Error() != "missing bearer token" {
		t.Errorf("no header: err = %v", err)
	}
	r.Header.Set("Authorization", "Bearer nope")
	if _, err := SharedKey("k").Authenticate(r); err == nil || err.Error() != "invalid api key" {
		t.Errorf("wrong key: err = %v", err)
	}
	r.Header.Set("Authorization", "Bearer k")
	p, err := SharedKey("k").Authenticate(r)
	if err != nil || p != operator {
		t.Errorf("right key: %+v, %v", p, err)
	}
	if p, err := SharedKey("").Authenticate(httptest.NewRequest(http.MethodGet, "/", nil)); err != nil || p != operator {
		t.Errorf("no key configured: %+v, %v", p, err)
	}
}

func TestJobsAreSeenOnlyByTheirAccount(t *testing.T) {
	ts := newTenants(t, Options{})

	rec := ts.send(t, keyA, `{"recipient":"+33600000000","body":"hi"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("send: %d %s", rec.Code, rec.Body)
	}
	id := decode[jobResponse](t, rec).JobID

	stored, err := ts.store.Jobs().Get(context.Background(), uuid.MustParse(id))
	if err != nil || stored.AccountID != ts.a {
		t.Fatalf("job account = %v (%v), want A", stored.AccountID, err)
	}

	for _, path := range []string{"/jobs/" + id, "/jobs/" + id + "/events"} {
		if rec := ts.as(t, keyB, http.MethodGet, path); rec.Code != http.StatusNotFound {
			t.Errorf("B GET %s = %d, want 404", path, rec.Code)
		}
		if rec := ts.as(t, keyA, http.MethodGet, path); rec.Code != http.StatusOK {
			t.Errorf("A GET %s = %d, want 200", path, rec.Code)
		}
	}

	if rec := ts.as(t, keyB, http.MethodDelete, "/jobs/"+id); rec.Code != http.StatusNotFound {
		t.Errorf("B DELETE = %d, want 404", rec.Code)
	}
	if j, _ := ts.store.Jobs().Get(context.Background(), uuid.MustParse(id)); j.Status != store.JobPending {
		t.Errorf("B's cancel went through: status %s", j.Status)
	}

	for key, want := range map[string]int{keyA: 1, keyB: 0, testAPIKey: 1} {
		got := decode[listJobsResponse](t, ts.as(t, key, http.MethodGet, "/jobs"))
		if len(got.Jobs) != want {
			t.Errorf("%s lists %d jobs, want %d", key, len(got.Jobs), want)
		}
		stats := decode[statsResponse](t, ts.as(t, key, http.MethodGet, "/stats"))
		if stats.Total != want {
			t.Errorf("%s stats total %d, want %d", key, stats.Total, want)
		}
	}
}

func TestDevicesAreSeenOnlyByTheirAccount(t *testing.T) {
	ts := newTenants(t, Options{})
	dev := ts.device(t, ts.a)
	path := "/devices/" + dev.ID.String()

	for key, want := range map[string]int{keyA: 1, keyB: 0, testAPIKey: 1} {
		got := decode[struct {
			Devices []deviceResponse `json:"devices"`
		}](t, ts.as(t, key, http.MethodGet, "/devices"))
		if len(got.Devices) != want {
			t.Errorf("%s lists %d devices, want %d", key, len(got.Devices), want)
		}
	}

	for _, c := range []struct{ method, path, body string }{
		{http.MethodPatch, path, `{"enabled":false}`},
		{http.MethodPut, path + "/policy", `{}`},
		{http.MethodDelete, path, ""},
	} {
		if rec := ts.withBody(t, keyB, c.method, c.path, c.body); rec.Code != http.StatusNotFound {
			t.Errorf("B %s %s = %d, want 404", c.method, c.path, rec.Code)
		}
	}
	got, err := ts.store.Devices().Get(context.Background(), dev.ID)
	if err != nil || !got.Enabled || got.Policy.Version != 0 {
		t.Fatalf("B changed A's device: %+v, %v", got, err)
	}

	if rec := ts.withBody(t, keyA, http.MethodPatch, path, `{"enabled":false}`); rec.Code != http.StatusOK {
		t.Errorf("A PATCH = %d, want 200", rec.Code)
	}
}

func TestSendingRefusesAnotherAccountsDevice(t *testing.T) {
	ts := newTenants(t, Options{})
	dev := ts.device(t, ts.a)
	ts.hub.ready = append(ts.hub.ready, readyState(dev.ID))

	body := `{"recipient":"+33600000000","body":"hi","deviceId":"` + dev.ID.String() + `"}`
	if rec := ts.send(t, keyB, body); rec.Code != http.StatusBadRequest {
		t.Errorf("B naming A's device = %d %s, want 400", rec.Code, rec.Body)
	}
	if rec := ts.send(t, keyA, body); rec.Code != http.StatusAccepted {
		t.Errorf("A naming its device = %d %s, want 202", rec.Code, rec.Body)
	}

	// A's ready phone does not make an immediate send possible for B.
	immediate := `{"recipient":"+33600000000","body":"hi","mode":"immediate"}`
	if rec := ts.send(t, keyB, immediate); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("B immediate = %d, want 503", rec.Code)
	}
	if rec := ts.send(t, keyA, immediate); rec.Code != http.StatusAccepted {
		t.Errorf("A immediate = %d, want 202", rec.Code)
	}
}

func TestEnrollmentTokensJoinTheMintersAccount(t *testing.T) {
	ts := newTenants(t, Options{})
	rec := ts.as(t, keyB, http.MethodPost, "/admin/devices/enroll-token")
	if rec.Code != http.StatusCreated {
		t.Fatalf("mint: %d %s", rec.Code, rec.Body)
	}
	plain := decode[enrollTokenResponse](t, rec).Token
	tok, err := ts.store.Enrollments().Lookup(context.Background(), token.SHA256{}.Hash(plain))
	if err != nil || tok.AccountID != ts.b {
		t.Fatalf("token account = %v (%v), want B", tok.AccountID, err)
	}
}

func TestEventsStreamCarriesOnlyTheAccountsEvents(t *testing.T) {
	broker := events.New()
	ts := newTenants(t, Options{Events: broker})
	srv := httptest.NewServer(ts.handler)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/events", nil)
	req.Header.Set("Authorization", "Bearer "+keyB)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()
	r := bufio.NewReader(resp.Body)
	readEvent(t, r) // ready

	// A's event, then one with no account, then B's: only B's comes through,
	// and it comes first.
	broker.Publish(events.Event{Type: events.DeviceUpdated, DeviceID: "a", AccountID: ts.a})
	broker.Publish(events.Event{Type: events.DeviceUpdated, DeviceID: "nobody"})
	broker.Publish(events.Event{Type: events.DeviceUpdated, DeviceID: "b", AccountID: ts.b})
	if _, data := readEvent(t, r); data == "" || !strings.Contains(data, `"deviceId":"b"`) {
		t.Fatalf("B received %s, want only its own event", data)
	}
}

func (h *harness) send(t *testing.T, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	return h.withBody(t, key, http.MethodPost, "/send", body)
}

func (h *harness) withBody(t *testing.T, key, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}
