package policy

import (
	"errors"
	"strings"
	"testing"
)

func TestInboundForwards(t *testing.T) {
	off := Inbound{Senders: []string{"OrangeMoney"}}
	all := Inbound{Enabled: true}
	some := Inbound{Enabled: true, Senders: []string{"OrangeMoney", "+22670000000"}}

	cases := []struct {
		rules  Inbound
		sender string
		want   bool
	}{
		{off, "OrangeMoney", false},
		{all, "anyone", true},
		{some, "orangemoney", true},
		{some, " OrangeMoney ", true},
		{some, "+22670000000", true},
		{some, "MoovMoney", false},
		{some, "OrangeMoney2", false},
	}
	for _, c := range cases {
		if got := c.rules.Forwards(c.sender); got != c.want {
			t.Errorf("%+v.Forwards(%q) = %v, want %v", c.rules, c.sender, got, c.want)
		}
	}
}

func TestInboundNormalizeAndValidate(t *testing.T) {
	p := Normalize(Policy{Inbound: Inbound{Enabled: true, Senders: []string{" OrangeMoney", "orangemoney", "", "Moov"}}})
	if got := strings.Join(p.Inbound.Senders, ","); got != "OrangeMoney,Moov" {
		t.Errorf("senders = %q, want trimmed and de-duplicated", got)
	}

	long := Policy{Inbound: Inbound{Senders: []string{strings.Repeat("x", maxSenderLen+1)}}}
	if err := Validate(long); !errors.Is(err, ErrInvalid) {
		t.Errorf("Validate(long sender) = %v, want ErrInvalid", err)
	}
	many := Policy{Inbound: Inbound{Senders: make([]string, maxSenders+1)}}
	if err := Validate(many); !errors.Is(err, ErrInvalid) {
		t.Errorf("Validate(too many senders) = %v, want ErrInvalid", err)
	}

	back := FromProto(ToProto(Policy{Inbound: Inbound{Enabled: true, Senders: []string{"OrangeMoney"}}}))
	if !back.Inbound.Enabled || len(back.Inbound.Senders) != 1 {
		t.Errorf("inbound lost on the wire: %+v", back.Inbound)
	}
}
