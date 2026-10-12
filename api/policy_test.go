package api

import (
	"net/http"
	"testing"

	"github.com/KGMA74/relaix-server/policy"
	"github.com/KGMA74/relaix-server/store"
)

func TestPutPolicyStoresVersionsAndPushes(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	dev := h.store.SeedDevice(&store.Device{Label: "p", Enabled: true})

	body := map[string]any{
		"availability": map[string]any{"timeZone": "Africa/Ouagadougou", "windows": []any{map[string]any{"days": []int{1, 2, 3, 4, 5}, "start": 480, "end": 1200}}},
		"accept":       map[string]any{"countries": []string{"bf"}, "pattern": `^\+2267`},
		"sims":         []any{map[string]any{"subscriptionId": 2, "disabled": true}},
	}
	rec := h.do(t, http.MethodPut, "/devices/"+dev.ID.String()+"/policy", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := decode[policy.Policy](t, rec)
	if got.Version != 1 || len(got.Accept.Countries) != 1 || got.Accept.Countries[0] != "BF" {
		t.Errorf("stored = %+v (want version 1, country normalised to BF)", got)
	}

	// The phone was sent the stored copy.
	if len(h.hub.sent) != 1 || h.hub.sent[0].msg.GetPolicy().GetVersion() != 1 {
		t.Errorf("pushed %d messages, want the stored policy once", len(h.hub.sent))
	}

	// And the device listing carries it.
	list := decode[struct {
		Devices []deviceResponse `json:"devices"`
	}](t, h.do(t, http.MethodGet, "/devices", nil))
	if len(list.Devices) != 1 || list.Devices[0].Policy.Version != 1 {
		t.Errorf("listing policy = %+v", list.Devices)
	}
}

func TestPutPolicyRejectsInvalidRules(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	dev := h.store.SeedDevice(&store.Device{Label: "p", Enabled: true})

	for name, body := range map[string]any{
		"unknown country": map[string]any{"accept": map[string]any{"countries": []string{"ZZ"}}},
		"broken pattern":  map[string]any{"accept": map[string]any{"pattern": "("}},
		"bad window":      map[string]any{"availability": map[string]any{"windows": []any{map[string]any{"start": 2000}}}},
		"unknown zone":    map[string]any{"availability": map[string]any{"timeZone": "Mars/Base"}},
	} {
		rec := h.do(t, http.MethodPut, "/devices/"+dev.ID.String()+"/policy", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, rec.Code)
		}
	}
	if len(h.hub.sent) != 0 {
		t.Error("invalid rules were pushed to the phone")
	}
}

func TestPutPolicyUnknownDevice(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	rec := h.do(t, http.MethodPut, "/devices/00000000-0000-0000-0000-000000000001/policy", map[string]any{})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
}

func TestCountries(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	got := decode[struct {
		Countries []policy.Country `json:"countries"`
	}](t, h.do(t, http.MethodGet, "/countries", nil))
	found := false
	for _, c := range got.Countries {
		if c.Code == "BF" && len(c.Prefixes) == 1 && c.Prefixes[0] == "226" {
			found = true
		}
	}
	if !found || len(got.Countries) < 200 {
		t.Errorf("countries: %d entries, BF found %v", len(got.Countries), found)
	}
}
