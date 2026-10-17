// Package library is the catalogue of ready-made automations: templates an
// account installs on a phone by filling in a few parameters, the way one adds
// a plugin. The gateway ships a built-in set; an embedding program (the cloud)
// adds the ones its administrators publish.
package library

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/KGMA74/relaix-server/policy"
)

// Template is an automation with parameters left to fill.
type Template struct {
	ID string `json:"id"`
	// Name and Description say what it does: they are what an operator and an
	// assistant choose by.
	Name        string `json:"name"`
	Description string `json:"description"`
	// Category groups the catalogue: "Mobile money", "Réponses"…
	Category string  `json:"category,omitempty"`
	Params   []Param `json:"params,omitempty"`
	// Automation is installed with every {{key}} replaced by its parameter.
	// Its own {field} templates are left for the phone.
	Automation policy.Automation `json:"automation"`
	// Builtin is set on the gateway's own templates, which nobody edits.
	Builtin bool `json:"builtin,omitempty"`
}

// Param is a value asked for at install time.
type Param struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Example     string `json:"example,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// Source lists the templates an account may install.
type Source interface {
	Templates(ctx context.Context) ([]Template, error)
}

//go:embed builtin.json
var builtinJSON []byte

var builtin = func() []Template {
	var list []Template
	if err := json.Unmarshal(builtinJSON, &list); err != nil {
		panic("library: builtin.json: " + err.Error())
	}
	for i := range list {
		list[i].Builtin = true
	}
	return list
}()

// Builtin is the gateway's own catalogue.
func Builtin() []Template { return append([]Template(nil), builtin...) }

// BuiltinSource serves the gateway's own catalogue.
type BuiltinSource struct{}

// Templates implements Source.
func (BuiltinSource) Templates(context.Context) ([]Template, error) { return Builtin(), nil }

// ErrInvalid wraps every problem with a template or its parameters.
var ErrInvalid = errors.New("invalid template")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

var (
	keyRe         = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,31}$`)
	placeholderRe = regexp.MustCompile(`\{\{\s*([a-zA-Z][a-zA-Z0-9_]*)\s*\}\}`)
)

const maxParams = 10

// Validate checks t as an administrator saves it: its parameters are well
// formed and used, and the automation it installs is valid once filled with
// their examples.
func (t Template) Validate() error {
	if strings.TrimSpace(t.Name) == "" || len([]rune(t.Name)) > 64 {
		return invalid("name: 1 to 64 characters")
	}
	if strings.TrimSpace(t.Description) == "" || len([]rune(t.Description)) > 500 {
		return invalid("description: 1 to 500 characters — it is how the template is chosen")
	}
	if len(t.Params) > maxParams {
		return invalid("params: at most %d", maxParams)
	}
	declared := map[string]bool{}
	example := map[string]string{}
	for i, p := range t.Params {
		if !keyRe.MatchString(p.Key) {
			return invalid("params[%d].key: a letter, then letters, digits or _", i)
		}
		if declared[p.Key] {
			return invalid("params[%d].key: %q declared twice", i, p.Key)
		}
		if strings.TrimSpace(p.Label) == "" {
			return invalid("params[%d].label: required", i)
		}
		declared[p.Key] = true
		example[p.Key] = p.Example
		if example[p.Key] == "" {
			example[p.Key] = "x"
		}
	}
	used := map[string]bool{}
	for _, k := range placeholders(t.Automation) {
		if !declared[k] {
			return invalid("{{%s}} is used but not declared in params", k)
		}
		used[k] = true
	}
	for _, p := range t.Params {
		if !used[p.Key] {
			return invalid("param %q is never used as {{%s}}", p.Key, p.Key)
		}
	}
	a := fill(t.Automation, example)
	if a.Name == "" {
		a.Name = t.Name
	}
	if err := policy.Validate(policy.Normalize(policy.Policy{Automations: []policy.Automation{a}})); err != nil {
		return invalid("automation: %v", err)
	}
	return nil
}

// Instantiate fills t's parameters and returns the automation to install: a
// fresh id, enabled, named and described after the template unless it says
// otherwise. Missing required values and unknown keys are refused.
func (t Template) Instantiate(values map[string]string) (policy.Automation, error) {
	known := map[string]bool{}
	for _, p := range t.Params {
		known[p.Key] = true
		if p.Required && strings.TrimSpace(values[p.Key]) == "" {
			return policy.Automation{}, invalid("%s (%s) is required", p.Key, p.Label)
		}
	}
	for k := range values {
		if !known[k] {
			return policy.Automation{}, invalid("no parameter %q in this template", k)
		}
	}
	trimmed := map[string]string{}
	for k, v := range values {
		trimmed[k] = strings.TrimSpace(v)
	}
	a := fill(t.Automation, trimmed)
	a.ID = ""
	a.Enabled = true
	if a.Name == "" {
		a.Name = t.Name
	}
	if a.Description == "" {
		a.Description = t.Description
	}
	return a, nil
}

// placeholders lists the {{key}} a template's automation uses.
func placeholders(a policy.Automation) []string {
	var keys []string
	each(&a, func(s *string, _ bool) {
		for _, m := range placeholderRe.FindAllStringSubmatch(*s, -1) {
			keys = append(keys, m[1])
		}
	})
	return keys
}

// fill replaces every {{key}} in a copy of a.
func fill(a policy.Automation, values map[string]string) policy.Automation {
	a.Senders = append([]string(nil), a.Senders...)
	a.Conditions = append([]policy.Condition(nil), a.Conditions...)
	a.Actions = append([]policy.Action(nil), a.Actions...)
	each(&a, func(s *string, pattern bool) {
		*s = placeholderRe.ReplaceAllStringFunc(*s, func(m string) string {
			v := values[placeholderRe.FindStringSubmatch(m)[1]]
			// In the pattern a value is matched as typed: "C++" is not a regexp.
			if pattern {
				return regexp.QuoteMeta(v)
			}
			return v
		})
	})
	// A sender left empty by an optional parameter means "any sender".
	senders := a.Senders[:0]
	for _, s := range a.Senders {
		if strings.TrimSpace(s) != "" {
			senders = append(senders, s)
		}
	}
	a.Senders = senders
	return a
}

// each visits every text of a that a parameter may appear in, saying which
// one is the pattern.
func each(a *policy.Automation, f func(s *string, pattern bool)) {
	f(&a.Pattern, true)
	for _, s := range []*string{&a.Name, &a.Description} {
		f(s, false)
	}
	for i := range a.Senders {
		f(&a.Senders[i], false)
	}
	for i := range a.Conditions {
		f(&a.Conditions[i].Value, false)
	}
	for i := range a.Actions {
		act := &a.Actions[i]
		for _, s := range []*string{&act.To, &act.Text, &act.URL, &act.Body, &act.Code} {
			f(s, false)
		}
	}
}

// Merge puts the built-in templates first, then the others, an id met twice
// keeping its first.
func Merge(lists ...[]Template) []Template {
	seen := map[string]bool{}
	var out []Template
	for _, l := range lists {
		for _, t := range l {
			if !seen[t.ID] {
				seen[t.ID] = true
				out = append(out, t)
			}
		}
	}
	return out
}
