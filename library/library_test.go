package library

import (
	"errors"
	"testing"

	"github.com/KGMA74/relaix-server/policy"
)

func TestBuiltinTemplatesAreValid(t *testing.T) {
	seen := map[string]bool{}
	for _, tpl := range Builtin() {
		if seen[tpl.ID] {
			t.Errorf("%s: id used twice", tpl.ID)
		}
		seen[tpl.ID] = true
		if err := tpl.Validate(); err != nil {
			t.Errorf("%s: %v", tpl.ID, err)
		}
	}
}

func TestInstantiate(t *testing.T) {
	tpl := find(t, "builtin-keyword-reply")
	a, err := tpl.Instantiate(map[string]string{"keyword": " C++ ", "reply": "Bonjour"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "Réponse à « C++ »" || a.Pattern != `(?i)C\+\+` || a.Actions[0].Text != "Bonjour" || !a.Enabled || a.ID != "" {
		t.Fatalf("got %+v", a)
	}
	if a.Description != tpl.Description {
		t.Errorf("description = %q", a.Description)
	}
	// The phone's own {sender} is left alone.
	if a.Actions[0].To != "{sender}" {
		t.Errorf("to = %q", a.Actions[0].To)
	}
	if err := policy.Validate(policy.Normalize(policy.Policy{Automations: []policy.Automation{a}})); err != nil {
		t.Fatal(err)
	}

	if _, err := tpl.Instantiate(map[string]string{"keyword": "INFO"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("missing reply: %v", err)
	}
	if _, err := tpl.Instantiate(map[string]string{"keyword": "INFO", "reply": "x", "other": "y"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown key: %v", err)
	}
}

func TestValidateCatchesUndeclaredAndUnusedParams(t *testing.T) {
	tpl := Template{Name: "T", Description: "D", Automation: policy.Automation{
		Name: "T", Actions: []policy.Action{{Type: policy.ActionWebhook, URL: "{{url}}"}},
	}}
	if err := tpl.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("undeclared {{url}}: %v", err)
	}
	tpl.Params = []Param{{Key: "url", Label: "URL", Example: "https://exemple.com"}, {Key: "unused", Label: "U"}}
	if err := tpl.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("unused param: %v", err)
	}
	tpl.Params = tpl.Params[:1]
	if err := tpl.Validate(); err != nil {
		t.Errorf("valid template refused: %v", err)
	}
}

func find(t *testing.T, id string) Template {
	t.Helper()
	for _, tpl := range Builtin() {
		if tpl.ID == id {
			return tpl
		}
	}
	t.Fatalf("no template %s", id)
	return Template{}
}
