package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/devices"
	"github.com/KGMA74/relaix-server/store"
)

type guardFunc func(ctx context.Context, tx store.Store, p Principal, jobs []*store.Job) error

func (f guardFunc) AdmitSend(ctx context.Context, tx store.Store, p Principal, jobs []*store.Job) error {
	return f(ctx, tx, p, jobs)
}

func TestSendGuardRefusalRollsTheSendBack(t *testing.T) {
	var seen []*store.Job
	h := newHarness(t, Options{APIKey: testAPIKey, SendGuard: guardFunc(
		func(_ context.Context, _ store.Store, p Principal, jobs []*store.Job) error {
			seen = jobs
			return &Refusal{Status: http.StatusPaymentRequired, Code: "insufficient_credits", Message: "top up first"}
		})})

	rec := h.send(t, testAPIKey, `{"recipients":["+33600000000","+33600000001"],"body":"hi"}`)
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status %d, want 402: %s", rec.Code, rec.Body)
	}
	if got := decode[errorBody](t, rec).Error; got.Code != "insufficient_credits" || got.Message != "top up first" {
		t.Errorf("error = %+v", got)
	}
	if len(seen) != 2 || seen[0].ID == uuid.Nil {
		t.Errorf("guard saw %d jobs (first id %v), want both, created", len(seen), seen[0].ID)
	}
	if jobs, _ := h.store.Jobs().List(context.Background(), store.JobFilter{Limit: 10}); len(jobs) != 0 {
		t.Errorf("%d jobs left behind by a refused send", len(jobs))
	}
}

func TestSendGuardFailureIsInternal(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey, SendGuard: guardFunc(
		func(context.Context, store.Store, Principal, []*store.Job) error { return errors.New("ledger down") })})
	if rec := h.send(t, testAPIKey, `{"recipient":"+33600000000","body":"hi"}`); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
}

func TestSendGuardAdmits(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey, SendGuard: guardFunc(
		func(context.Context, store.Store, Principal, []*store.Job) error { return nil })})
	if rec := h.send(t, testAPIKey, `{"recipient":"+33600000000","body":"hi"}`); rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202", rec.Code)
	}
}

func TestRoutesAreMountedBehindAuthentication(t *testing.T) {
	h := newHarness(t, Options{
		APIKey: testAPIKey,
		Routes: func(mux *http.ServeMux) {
			mux.HandleFunc("GET /v1/whoami", func(w http.ResponseWriter, r *http.Request) {
				p, ok := PrincipalFrom(r.Context())
				writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "all": p.AllAccounts})
			})
			mux.HandleFunc("POST /auth/login", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})
		},
		PublicPrefixes: []string{"/auth/"},
	})

	if rec := h.as(t, "wrong", http.MethodGet, "/v1/whoami"); rec.Code != http.StatusUnauthorized {
		t.Errorf("extra route without the key = %d, want 401", rec.Code)
	}
	rec := h.as(t, testAPIKey, http.MethodGet, "/v1/whoami")
	if got := decode[map[string]bool](t, rec); !got["ok"] || !got["all"] {
		t.Errorf("principal in extra route = %v", got)
	}

	req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	rec = httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("public prefix without credentials = %d, want 204", rec.Code)
	}
}

func TestUIConfigIsPublic(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui-config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if got := decode[UIConfig](t, rec); got.Name != "Relaix" || got.Features == nil || len(got.Features) != 0 {
		t.Errorf("default config = %+v", got)
	}

	h = newHarness(t, Options{APIKey: testAPIKey, UIConfig: UIConfig{Name: "Acme SMS", Features: []string{"billing"}}})
	rec = httptest.NewRecorder()
	h.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui-config", nil))
	if got := decode[UIConfig](t, rec); got.Name != "Acme SMS" || len(got.Features) != 1 {
		t.Errorf("custom config = %+v", got)
	}
}

type refusingEnroller struct{}

func (refusingEnroller) MintEnrollmentToken(context.Context, uuid.UUID) (*devices.MintedToken, error) {
	return nil, &devices.RefusedError{Reason: "device limit reached"}
}

func TestMintRefusalIsForbiddenWithTheReason(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	h.handler = New(h.store, h.hub, refusingEnroller{}, Options{APIKey: testAPIKey}).Handler()
	rec := h.as(t, testAPIKey, http.MethodPost, "/admin/devices/enroll-token")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", rec.Code)
	}
	if got := decode[errorBody](t, rec).Error; got.Code != "enrollment_refused" || got.Message != "device limit reached" {
		t.Errorf("error = %+v", got)
	}
}
