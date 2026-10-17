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
	"slices"
	"strconv"
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
	// Payment makes the template a payment method: what a customer dials to
	// pay, and which of the automation's fields say what was paid.
	Payment *PaymentSpec `json:"payment,omitempty"`
}

// PaymentSpec turns a template into a way to be paid. The automation it
// installs forwards the operator's SMS announcing a payment; the fields named
// here say the amount, the operator's transaction id and who paid.
type PaymentSpec struct {
	// Operator and Country name it to the customer: "Orange Money", "BF".
	Operator string `json:"operator"`
	Country  string `json:"country,omitempty"`
	Currency string `json:"currency"`
	// Dial is what the customer dials, with the template's {{params}} (the
	// merchant code) and {amount}: "*144*10*{{merchant}}*{amount}#".
	Dial string `json:"dial"`
	// Instructions are shown to the customer under the code.
	Instructions string `json:"instructions,omitempty"`
	// Fields maps the automation's fields: amount, transId, payer.
	Fields PaymentFields `json:"fields"`
}

// PaymentFields names the automation's fields that carry a payment.
type PaymentFields struct {
	Amount  string `json:"amount"`
	TransID string `json:"transId"`
	Payer   string `json:"payer,omitempty"`
}

var ussdCode = regexp.MustCompile(`^[*#][0-9*#]+#$`)

// DialFor is the code a customer dials for amount, the template's values
// filled in.
func (p PaymentSpec) DialFor(values map[string]string, amount int64) string {
	code := placeholderRe.ReplaceAllStringFunc(p.Dial, func(m string) string {
		return strings.TrimSpace(values[placeholderRe.FindStringSubmatch(m)[1]])
	})
	return strings.ReplaceAll(code, "{amount}", strconv.FormatInt(amount, 10))
}

func (p PaymentSpec) validate(a policy.Automation, example map[string]string) error {
	if strings.TrimSpace(p.Operator) == "" || len(p.Currency) != 3 {
		return invalid("payment: an operator and a three-letter currency (XOF)")
	}
	if !strings.Contains(p.Dial, "{amount}") {
		return invalid("payment.dial: the code the customer dials, with {amount}, e.g. *144*10*{{merchant}}*{amount}#")
	}
	if code := p.DialFor(example, 1000); !ussdCode.MatchString(code) {
		return invalid("payment.dial: %q is not a USSD code once filled in", code)
	}
	if !a.Forwards() {
		return invalid("payment: the automation must forward the operator's SMS (a forward action)")
	}
	fields := a.Fields()
	for _, f := range []struct{ name, field string }{{"amount", p.Fields.Amount}, {"transId", p.Fields.TransID}, {"payer", p.Fields.Payer}} {
		if f.field == "" && f.name == "payer" {
			continue
		}
		if !slices.Contains(fields, f.field) {
			return invalid("payment.fields.%s: %q is not a field of the automation's pattern", f.name, f.field)
		}
	}
	return nil
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
	// The customer's code may use a param the automation does not: the
	// merchant code, say.
	if t.Payment != nil {
		for _, k := range placeholderRe.FindAllStringSubmatch(t.Payment.Dial, -1) {
			if !declared[k[1]] {
				return invalid("payment.dial: {{%s}} is used but not declared in params", k[1])
			}
			used[k[1]] = true
		}
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
	if t.Payment != nil {
		return t.Payment.validate(a, example)
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
