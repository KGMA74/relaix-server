package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func dashboardFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":         {Data: []byte("<!doctype html><div id=root></div>")},
		"favicon.svg":        {Data: []byte("<svg/>")},
		"assets/app-abc.js":  {Data: []byte("console.log(1)")},
		"assets/app-abc.css": {Data: []byte("body{}")},
	}
}

// get sends an unauthenticated GET, as a browser loading the dashboard would.
func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestDashboardServesFilesWithoutAKey(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey, UI: dashboardFS()})

	rec := get(h.handler, "/ui/assets/app-abc.js")
	if rec.Code != http.StatusOK || rec.Body.String() != "console.log(1)" {
		t.Fatalf("asset: status %d body %q", rec.Code, rec.Body)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("fingerprinted asset Cache-Control = %q, want immutable", cc)
	}

	rec = get(h.handler, "/ui/favicon.svg")
	if rec.Code != http.StatusOK {
		t.Fatalf("favicon: status %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("unfingerprinted file Cache-Control = %q, want no-cache", cc)
	}
}

func TestDashboardFallsBackToIndexForClientRoutes(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey, UI: dashboardFS()})

	for _, p := range []string{"/ui/", "/ui/history", "/ui/devices/123"} {
		rec := get(h.handler, p)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "id=root") {
			t.Errorf("%s: status %d body %q, want index.html", p, rec.Code, rec.Body)
			continue
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s: index Cache-Control = %q, want no-cache", p, cc)
		}
		if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
			t.Errorf("%s: CSP = %q", p, csp)
		}
	}
}

func TestDashboardMissingAssetIsA404(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey, UI: dashboardFS()})

	if rec := get(h.handler, "/ui/assets/gone-123.js"); rec.Code != http.StatusNotFound {
		t.Errorf("missing asset: status %d, want 404 rather than index.html", rec.Code)
	}
}

func TestDashboardRootRedirects(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey, UI: dashboardFS()})

	rec := get(h.handler, "/")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/ui/" {
		t.Errorf("/: status %d location %q, want 302 to /ui/", rec.Code, rec.Header().Get("Location"))
	}
}

func TestDashboardWithoutTrailingSlashRedirects(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey, UI: dashboardFS()})

	rec := get(h.handler, "/ui")
	if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != "/ui/" {
		t.Errorf("/ui: status %d location %q, want 307 to /ui/", rec.Code, rec.Header().Get("Location"))
	}
}

func TestDashboardDoesNotOpenTheAPI(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey, UI: dashboardFS()})

	// The dashboard's routes are public; the data behind them is not.
	for _, p := range []string{"/jobs", "/devices", "/stats"} {
		if rec := get(h.handler, p); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without a key: status %d, want 401", p, rec.Code)
		}
	}
}

func TestDashboardNotBuilt(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey, UI: fstest.MapFS{".gitkeep": {}}})

	rec := get(h.handler, "/ui/")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "make ui") {
		t.Errorf("unbuilt dashboard: status %d body %q", rec.Code, rec.Body)
	}
}

func TestNoDashboardWithoutUI(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})

	if rec := get(h.handler, "/ui/"); rec.Code != http.StatusNotFound {
		t.Errorf("/ui/ with no UI configured: status %d, want 404", rec.Code)
	}
}
