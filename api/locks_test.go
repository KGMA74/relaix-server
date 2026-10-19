package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/policy"
	"github.com/KGMA74/relaix-server/store"
)

func TestLockedAutomationsStayAsTheyAre(t *testing.T) {
	locks := policy.Locks{}
	h := newHarness(t, Options{APIKey: testAPIKey, Locks: func(context.Context, uuid.UUID) (policy.Locks, error) { return locks, nil }})
	dev := h.store.SeedDevice(&store.Device{Label: "p", Enabled: true})
	path := "/devices/" + dev.ID.String() + "/policy"

	paid := map[string]any{"id": "pay-1", "name": "Orange Money", "enabled": true, "senders": []string{"OrangeMoney"},
		"actions": []any{map[string]any{"type": "forward"}}}
	other := map[string]any{"id": "own", "name": "Mine", "enabled": true, "actions": []any{map[string]any{"type": "forward"}}}
	if rec := h.do(t, http.MethodPut, path, map[string]any{"automations": []any{paid, other}}); rec.Code != http.StatusOK {
		t.Fatalf("install: %d %s", rec.Code, rec.Body)
	}
	locks["pay-1"] = "payment method Orange Money"

	// Listed for the dashboard.
	got := decode[struct {
		Locks map[string]policy.Locks `json:"locks"`
	}](t, h.do(t, http.MethodGet, "/automations/locks", nil))
	if got.Locks[dev.ID.String()]["pay-1"] == "" {
		t.Errorf("locks = %v", got.Locks)
	}

	// The rest of the rules may change; the locked one as it is, too.
	other["name"] = "Mine, renamed"
	if rec := h.do(t, http.MethodPut, path, map[string]any{"automations": []any{paid, other}}); rec.Code != http.StatusOK {
		t.Fatalf("editing another automation: %d %s", rec.Code, rec.Body)
	}

	changed := map[string]any{"id": "pay-1", "name": "Orange Money", "enabled": false, "senders": []string{"OrangeMoney"},
		"actions": []any{map[string]any{"type": "forward"}}}
	for name, list := range map[string][]any{"changed": {changed, other}, "removed": {other}} {
		rec := h.do(t, http.MethodPut, path, map[string]any{"automations": list})
		if rec.Code != http.StatusConflict {
			t.Errorf("%s: status %d, want 409", name, rec.Code)
		}
	}

	// The program that locked it removes it, past the lock.
	stored, err := h.server.ReplacePolicy(context.Background(), dev.ID, func(p policy.Policy) policy.Policy {
		p.Automations = p.Automations[1:]
		return p
	})
	if err != nil || len(stored.Automations) != 1 || stored.Automations[0].ID != "own" {
		t.Fatalf("ReplacePolicy = %+v, %v", stored.Automations, err)
	}
	if last := h.hub.sent[len(h.hub.sent)-1]; len(last.msg.GetPolicy().GetAutomations()) != 1 {
		t.Error("the phone was not sent the rules without it")
	}
}
