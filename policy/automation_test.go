package policy

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

type vector struct {
	Name       string            `json:"name"`
	Automation Automation        `json:"automation"`
	SMS        vectorSMS         `json:"sms"`
	Match      bool              `json:"match"`
	Fields     map[string]string `json:"fields"`
	Template   string            `json:"template"`
	Rendered   string            `json:"rendered"`
}

type vectorSMS struct {
	Sender     string `json:"sender"`
	Body       string `json:"body"`
	ReceivedAt string `json:"receivedAt"`
	SIM        int32  `json:"sim"`
}

func TestAutomationVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/automation_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct{ Cases []vector }
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	for _, v := range file.Cases {
		t.Run(v.Name, func(t *testing.T) {
			fields, ok := v.Automation.Match(v.SMS.Sender, v.SMS.Body, v.SMS.ReceivedAt, v.SMS.SIM)
			if ok != v.Match {
				t.Fatalf("match = %v, want %v", ok, v.Match)
			}
			for k, want := range v.Fields {
				if fields[k] != want {
					t.Errorf("field %s = %q, want %q", k, fields[k], want)
				}
			}
			if v.Template != "" {
				if got := Render(v.Template, fields); got != v.Rendered {
					t.Errorf("rendered %q, want %q", got, v.Rendered)
				}
			}
		})
	}
}

func valid() Automation {
	return Automation{
		Name: "paiement", Enabled: true, Senders: []string{"OrangeMoney"},
		Pattern: `recu (?<amount>[0-9 ]+) FCFA de (?<from>[0-9]+)`,
		Actions: []Action{{Type: ActionForward}},
	}
}

func TestAutomationValidation(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Automation)
		want   string
	}{
		{"ok", func(*Automation) {}, ""},
		{"no name", func(a *Automation) { a.Name = "" }, "name"},
		{"bad regex", func(a *Automation) { a.Pattern = "(" }, "pattern"},
		{"lookahead is not RE2", func(a *Automation) { a.Pattern = "(?=x)" }, "pattern"},
		{"underscore group", func(a *Automation) { a.Pattern = "(?<the_amount>[0-9]+)" }, "group name"},
		{"group shadows a builtin", func(a *Automation) { a.Pattern = "(?<sender>.+)" }, "group name"},
		{"unknown field in condition", func(a *Automation) { a.Conditions = []Condition{{"total", "gt", "1"}} }, "not a field"},
		{"numeric op on text", func(a *Automation) { a.Conditions = []Condition{{"amount", "gt", "abc"}} }, "not a number"},
		{"bad op", func(a *Automation) { a.Conditions = []Condition{{"amount", "like", "1"}} }, "op"},
		{"no action", func(a *Automation) { a.Actions = nil }, "actions"},
		{"unknown action", func(a *Automation) { a.Actions = []Action{{Type: "email"}} }, "type"},
		{"reply to a field", func(a *Automation) {
			a.Actions = []Action{{Type: ActionReplySMS, To: "{sender}", Text: "Merci {amount}"}}
		}, ""},
		{"reply to a bad number", func(a *Automation) { a.Actions = []Action{{Type: ActionReplySMS, To: "70000000", Text: "x"}} }, "E.164"},
		{"reply with unknown field", func(a *Automation) {
			a.Actions = []Action{{Type: ActionReplySMS, To: "{sender}", Text: "{total}"}}
		}, "not a field"},
		{"pin outside ussd", func(a *Automation) {
			a.Actions = []Action{{Type: ActionReplySMS, To: "{sender}", Text: "{pin}"}}
		}, "not a field"},
		{"webhook", func(a *Automation) {
			a.Actions = []Action{{Type: ActionWebhook, URL: "https://x.test/h", Body: `{"a":"{amount}"}`}}
		}, ""},
		{"webhook bad url", func(a *Automation) { a.Actions = []Action{{Type: ActionWebhook, URL: "ftp://x"}} }, "url"},
		{"ussd with pin", func(a *Automation) { a.Actions = []Action{{Type: ActionUSSD, Code: "*144*2*1*{from}*{amount}*{pin}#"}} }, ""},
		{"ussd not a code", func(a *Automation) { a.Actions = []Action{{Type: ActionUSSD, Code: "144{pin}"}} }, "USSD"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := valid()
			c.change(&a)
			err := Validate(Normalize(Policy{Automations: []Automation{a}}))
			if c.want == "" {
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Validate = %v, want an error about %q", err, c.want)
			}
		})
	}
}

func TestAutomationsNormalizeAndWire(t *testing.T) {
	in := []Automation{valid(), valid()}
	in[1].ID = "keep"
	p := Normalize(Policy{Automations: in})
	if p.Automations[0].ID == "" || p.Automations[1].ID != "keep" {
		t.Errorf("ids = %q, %q", p.Automations[0].ID, p.Automations[1].ID)
	}
	if in[0].ID != "" {
		t.Error("Normalize edited the caller's automations")
	}

	a := valid()
	a.ID = "x"
	a.Conditions = []Condition{{"amount", "gte", "1000"}}
	a.Actions = []Action{
		{Type: ActionForward},
		{Type: ActionReplySMS, To: "{sender}", Text: "ok"},
		{Type: ActionWebhook, URL: "https://x.test", Body: "b", Secret: "s"},
		{Type: ActionUSSD, Code: "*1*{pin}#"},
	}
	back := FromProto(ToProto(Policy{Automations: []Automation{a}})).Automations
	if len(back) != 1 || len(back[0].Actions) != 4 || back[0].Actions[2].Secret != "s" || back[0].Actions[3].Code != "*1*{pin}#" ||
		len(back[0].Conditions) != 1 || back[0].Pattern != a.Pattern {
		t.Errorf("lost on the wire: %+v", back)
	}

	dup := []Automation{valid(), valid()}
	dup[0].ID, dup[1].ID = "same", "same"
	if err := Validate(Normalize(Policy{Automations: dup})); !errors.Is(err, ErrInvalid) {
		t.Errorf("duplicate ids = %v", err)
	}
}

func TestNumber(t *testing.T) {
	for in, want := range map[string]float64{"5 000": 5000, "5\u00a0000": 5000, "10,25": 10.25, "12.5": 12.5} {
		if got, ok := Number(in); !ok || got != want {
			t.Errorf("Number(%q) = %v %v, want %v", in, got, ok, want)
		}
	}
	if _, ok := Number("abc"); ok {
		t.Error("abc is not a number")
	}
}
