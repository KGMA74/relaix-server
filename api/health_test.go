package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// failingPinger stands in for an unreachable database.
type failingPinger struct{ err error }

func (p failingPinger) Ping(context.Context) error { return p.err }

// probe sends an unauthenticated request, which is how an orchestrator asks.
func probe(t *testing.T, h *harness, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

// Probes must answer without credentials, or the gateway gets restarted for
// being unauthenticated rather than for being unhealthy.
func TestProbesBypassAuthentication(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})

	for _, path := range []string{"/healthz", "/readyz"} {
		t.Run(path, func(t *testing.T) {
			if rec := probe(t, h, path); rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
			}
		})
	}

	// And everything else still requires the key.
	if rec := probe(t, h, "/devices"); rec.Code != http.StatusUnauthorized {
		t.Errorf("/devices without a key = %d, want 401", rec.Code)
	}
}

// Liveness answers "is this process wedged". Checking the database here would
// mean a database outage restarts every instance in a loop, turning somebody
// else's problem into an outage of our own.
func TestLivenessIgnoresTheDatabase(t *testing.T) {
	h := newHarness(t, Options{
		APIKey: testAPIKey,
		Pinger: failingPinger{err: errors.New("database is gone")},
	})

	if rec := probe(t, h, "/healthz"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even with a dead database", rec.Code)
	}
}

// Readiness answers "can this instance do its job", and without a database it
// cannot.
func TestReadinessFailsWhenTheDatabaseIsDown(t *testing.T) {
	h := newHarness(t, Options{
		APIKey: testAPIKey,
		Pinger: failingPinger{err: errors.New("connection refused")},
	})

	rec := probe(t, h, "/readyz")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}

	// The reason is logged, not returned: an unauthenticated endpoint must not
	// hand out driver internals or connection strings.
	if body := rec.Body.String(); strings.Contains(body, "connection refused") {
		t.Errorf("the probe leaked the driver error: %s", body)
	}
}

func TestReadinessSucceedsWithAHealthyDatabase(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey, Pinger: h1Pinger{}})

	if rec := probe(t, h, "/readyz"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
}

func TestReadinessWithoutAPingerIsOK(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})

	if rec := probe(t, h, "/readyz"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

type h1Pinger struct{}

func (h1Pinger) Ping(context.Context) error { return nil }
