package policy

import (
	"errors"
	"testing"
	"time"
)

// A Wednesday, 10:30 UTC.
var wed1030 = time.Date(2026, 10, 14, 10, 30, 0, 0, time.UTC)

func hm(h, m int) int { return h*60 + m }

func TestZeroPolicyAcceptsEverythingAlways(t *testing.T) {
	var p Policy
	c := p.Choose(wed1030, "+22670000000", Device{Sims: []int32{1, 2}}, 0)
	if !c.OK || c.SubscriptionID != 0 {
		t.Fatalf("zero policy: %+v, want OK on the default SIM", c)
	}
	if !p.AcceptsEver("+15550000000", Device{}, 0) {
		t.Fatal("zero policy refuses a number")
	}
}

func TestWindows(t *testing.T) {
	cases := []struct {
		name string
		w    Window
		at   time.Time
		open bool
	}{
		{"inside a weekday window", Window{Days: []int{1, 2, 3, 4, 5}, Start: hm(8, 0), End: hm(20, 0)}, wed1030, true},
		{"end is exclusive", Window{Start: hm(8, 0), End: hm(10, 30)}, wed1030, false},
		{"start is inclusive", Window{Start: hm(10, 30), End: hm(11, 0)}, wed1030, true},
		{"wrong day", Window{Days: []int{6, 7}, Start: hm(8, 0), End: hm(20, 0)}, wed1030, false},
		{"whole day", Window{Days: []int{3}}, wed1030, true},
		// 22:00-06:00 starting Tuesday covers Wednesday 05:00, not Wednesday 07:00.
		{"overnight, morning part", Window{Days: []int{2}, Start: hm(22, 0), End: hm(6, 0)},
			time.Date(2026, 10, 14, 5, 0, 0, 0, time.UTC), true},
		{"overnight, after it ends", Window{Days: []int{2}, Start: hm(22, 0), End: hm(6, 0)},
			time.Date(2026, 10, 14, 7, 0, 0, 0, time.UTC), false},
		{"overnight, evening part", Window{Days: []int{3}, Start: hm(22, 0), End: hm(6, 0)},
			time.Date(2026, 10, 14, 23, 0, 0, 0, time.UTC), true},
		// Sunday is 7, and the day before Monday.
		{"sunday overnight into monday", Window{Days: []int{7}, Start: hm(22, 0), End: hm(2, 0)},
			time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := Availability{Windows: []Window{tc.w}}
			if got := a.Open(tc.at, ""); got != tc.open {
				t.Errorf("Open = %v, want %v", got, tc.open)
			}
		})
	}
}

// The windows are read in the phone's zone: at 10:30 UTC, 08:00-20:00 is
// closed in New York (06:30) and open in Ouagadougou (10:30).
func TestWindowsUseTheZone(t *testing.T) {
	a := Availability{Windows: []Window{{Start: hm(8, 0), End: hm(20, 0)}}}
	if a.Open(wed1030, "America/New_York") {
		t.Error("open at 06:30 New York time")
	}
	if !a.Open(wed1030, "Africa/Ouagadougou") {
		t.Error("closed at 10:30 Ouagadougou time")
	}
	// The availability's own zone wins over the phone's.
	a.TimeZone = "Africa/Ouagadougou"
	if !a.Open(wed1030, "America/New_York") {
		t.Error("the availability's zone did not take precedence")
	}
	// An unknown zone reads as UTC rather than failing.
	if !a.Open(wed1030, "Not/AZone") || !(Availability{Windows: a.Windows, TimeZone: ""}).Open(wed1030, "Not/AZone") {
		t.Error("unknown zone did not fall back to UTC")
	}
}

func TestAcceptRules(t *testing.T) {
	cases := []struct {
		name      string
		r         AcceptRules
		recipient string
		want      bool
	}{
		{"country match", AcceptRules{Countries: []string{"BF"}}, "+22670000000", true},
		{"country mismatch", AcceptRules{Countries: []string{"BF"}}, "+22501000000", false},
		{"one of several", AcceptRules{Countries: []string{"CI", "BF"}}, "+22501000000", true},
		{"pattern", AcceptRules{Pattern: `^\+2267[0-9]`}, "+22670000000", true},
		{"pattern miss", AcceptRules{Pattern: `^\+2267[0-9]`}, "+22650000000", false},
		{"both must hold", AcceptRules{Countries: []string{"BF"}, Pattern: `^\+2265`}, "+22670000000", false},
		{"caribbean narrowed by area code", AcceptRules{Countries: []string{"AG"}}, "+12684640000", true},
		{"caribbean is not the US", AcceptRules{Countries: []string{"AG"}}, "+12125550000", false},
		{"kazakhstan vs russia", AcceptRules{Countries: []string{"KZ"}}, "+79001234567", false},
		{"broken stored pattern accepts nothing", AcceptRules{Pattern: `(`}, "+22670000000", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.r.Accepts(tc.recipient); got != tc.want {
				t.Errorf("Accepts(%s) = %v, want %v", tc.recipient, got, tc.want)
			}
		})
	}
}

func TestChooseSIM(t *testing.T) {
	d := Device{Sims: []int32{1, 2}} // 1 is the default
	p := Policy{Sims: []SimPolicy{
		{SubscriptionID: 1, Accept: AcceptRules{Countries: []string{"BF"}}},
		{SubscriptionID: 2, Accept: AcceptRules{Countries: []string{"CI"}}},
	}}

	if c := p.Choose(wed1030, "+22670000000", d, 0); !c.OK || c.SubscriptionID != 1 {
		t.Errorf("BF number: %+v, want SIM 1", c)
	}
	if c := p.Choose(wed1030, "+22501000000", d, 0); !c.OK || c.SubscriptionID != 2 {
		t.Errorf("CI number: %+v, want SIM 2", c)
	}
	if c := p.Choose(wed1030, "+33600000000", d, 0); c.OK || !c.Never {
		t.Errorf("FR number: %+v, want Never", c)
	}
	// A job naming a SIM is held to that SIM.
	if c := p.Choose(wed1030, "+22501000000", d, 1); c.OK || !c.Never {
		t.Errorf("CI number on SIM 1: %+v, want Never", c)
	}
	// A SIM with no rules of its own takes anything the device accepts.
	p.Sims = p.Sims[:1]
	if c := p.Choose(wed1030, "+33600000000", d, 0); !c.OK || c.SubscriptionID != 2 {
		t.Errorf("FR number with SIM 2 unruled: %+v, want SIM 2", c)
	}
}

func TestDisabledAndClosedSIMs(t *testing.T) {
	d := Device{Sims: []int32{1, 2}}
	p := Policy{Sims: []SimPolicy{
		{SubscriptionID: 1, Disabled: true},
		{SubscriptionID: 2, Availability: Availability{Windows: []Window{{Start: hm(20, 0), End: hm(22, 0)}}}},
	}}
	c := p.Choose(wed1030, "+22670000000", d, 0)
	if c.OK || c.Never {
		t.Errorf("only SIM closed now: %+v, want wait (not OK, not Never)", c)
	}
	if !p.AcceptsEver("+22670000000", d, 0) {
		t.Error("a closed SIM still accepts eventually")
	}
	p.Sims[1].Disabled = true
	if p.AcceptsEver("+22670000000", d, 0) {
		t.Error("all SIMs disabled, yet AcceptsEver")
	}
}

// Never must not depend on the device's own window being open.
func TestNeverIgnoresTheClock(t *testing.T) {
	d := Device{Sims: []int32{1}}
	p := Policy{
		Availability: Availability{Windows: []Window{{Start: hm(1, 0), End: hm(2, 0)}}},
		Sims:         []SimPolicy{{SubscriptionID: 1, Accept: AcceptRules{Countries: []string{"CI"}}}},
	}
	if p.AcceptsEver("+22670000000", d, 0) {
		t.Error("SIM refuses BF, yet AcceptsEver while the device window is closed")
	}
	if !p.AcceptsEver("+22501000000", d, 0) {
		t.Error("SIM accepts CI, yet not AcceptsEver")
	}
}

func TestValidate(t *testing.T) {
	bad := []Policy{
		{Availability: Availability{TimeZone: "Mars/Olympus"}},
		{Availability: Availability{Windows: []Window{{Start: -1}}}},
		{Availability: Availability{Windows: []Window{{Start: 0, End: 1441}}}},
		{Availability: Availability{Windows: []Window{{Days: []int{0}}}}},
		{Accept: AcceptRules{Countries: []string{"ZZ"}}},
		{Accept: AcceptRules{Pattern: "("}},
		{Sims: []SimPolicy{{SubscriptionID: 1}, {SubscriptionID: 1}}},
		{Sims: []SimPolicy{{SubscriptionID: 1, Accept: AcceptRules{Pattern: "["}}}},
	}
	for i, p := range bad {
		if err := Validate(Normalize(p)); !errors.Is(err, ErrInvalid) {
			t.Errorf("case %d: Validate = %v, want ErrInvalid", i, err)
		}
	}
	good := Policy{
		Availability: Availability{TimeZone: "Africa/Ouagadougou", Windows: []Window{{Days: []int{1, 5}, Start: hm(8, 0), End: hm(18, 0)}}},
		Accept:       AcceptRules{Countries: []string{"bf", " ci "}, Pattern: `^\+226`},
	}
	n := Normalize(good)
	if err := Validate(n); err != nil {
		t.Fatalf("Validate(good) = %v", err)
	}
	if got := n.Accept.Countries; len(got) != 2 || got[0] != "BF" || got[1] != "CI" {
		t.Errorf("countries not normalized: %v", got)
	}
}

func TestDeviceLimitIsNotNow(t *testing.T) {
	p := Policy{Limits: Limits{PerHour: 10, PerDay: 100}}
	d := Device{}

	d.Sent = Count{Hour: 9, Day: 50}
	if c := p.Choose(wed1030, "+22670000000", d, 0); !c.OK {
		t.Errorf("under the caps: %+v, want OK", c)
	}
	d.Sent = Count{Hour: 10, Day: 50}
	if c := p.Choose(wed1030, "+22670000000", d, 0); c.OK || c.Never {
		t.Errorf("at the hourly cap: %+v, want wait", c)
	}
	d.Sent = Count{Hour: 0, Day: 100}
	if c := p.Choose(wed1030, "+22670000000", d, 0); c.OK || c.Never {
		t.Errorf("at the daily cap: %+v, want wait", c)
	}
	if !p.AcceptsEver("+22670000000", d, 0) {
		t.Error("a capped device does not accept ever: a cap is temporary")
	}
}

// A SIM at its cap hands the job to the phone's other SIM.
func TestSIMLimitMovesToTheOtherSIM(t *testing.T) {
	p := Policy{Sims: []SimPolicy{
		{SubscriptionID: 1, Limits: Limits{PerHour: 5}},
		{SubscriptionID: 2, Limits: Limits{PerHour: 5}},
	}}
	d := Device{Sims: []int32{1, 2}, SimSent: map[int32]Count{1: {Hour: 5}}}
	if c := p.Choose(wed1030, "+22670000000", d, 0); !c.OK || c.SubscriptionID != 2 {
		t.Errorf("SIM 1 capped: %+v, want SIM 2", c)
	}
	d.SimSent[2] = Count{Hour: 5}
	if c := p.Choose(wed1030, "+22670000000", d, 0); c.OK || c.Never {
		t.Errorf("both capped: %+v, want wait", c)
	}
}

func TestValidateLimits(t *testing.T) {
	for i, l := range []Limits{{PerHour: -1}, {PerDay: -2}, {PerHour: 10, PerDay: 5}, {PerHour: maxLimit + 1}} {
		if err := Validate(Policy{Limits: l}); !errors.Is(err, ErrInvalid) {
			t.Errorf("case %d (%+v): %v, want ErrInvalid", i, l, err)
		}
		if err := Validate(Policy{Sims: []SimPolicy{{SubscriptionID: 1, Limits: l}}}); !errors.Is(err, ErrInvalid) {
			t.Errorf("SIM case %d (%+v): %v, want ErrInvalid", i, l, err)
		}
	}
	if err := Validate(Policy{Limits: Limits{PerHour: 30, PerDay: 300}}); err != nil {
		t.Errorf("valid limits rejected: %v", err)
	}
}
