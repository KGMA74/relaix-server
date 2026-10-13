package policy

import (
	"slices"
	"strings"

	v1 "github.com/KGMA74/relaix-server/gen/smsgateway/v1"
)

// Inbound says which received SMS a phone forwards. The phone applies it
// before anything leaves the handset; the server applies it again on arrival,
// so a phone holding an older copy cannot hand over what the rules no longer
// allow.
type Inbound struct {
	// Enabled forwards at all. Off, the zero value, forwards nothing.
	Enabled bool `json:"enabled,omitempty"`
	// Senders are matched case-insensitively and whole: an alphanumeric
	// sender id ("OrangeMoney") or an E.164 number. Empty, when Enabled:
	// every sender.
	Senders []string `json:"senders,omitempty"`
}

// Limits on the sender list: enough for every mobile-money and bank sender of
// a country, small enough to read on a phone.
const (
	maxSenders   = 50
	maxSenderLen = 32
)

// Forwards reports whether a message from sender is to be forwarded.
func (r Inbound) Forwards(sender string) bool {
	if !r.Enabled {
		return false
	}
	if len(r.Senders) == 0 {
		return true
	}
	sender = strings.TrimSpace(sender)
	return slices.ContainsFunc(r.Senders, func(s string) bool { return strings.EqualFold(s, sender) })
}

func normalizeInbound(r Inbound) Inbound {
	var senders []string
	for _, s := range r.Senders {
		s = strings.TrimSpace(s)
		if s != "" && !slices.ContainsFunc(senders, func(o string) bool { return strings.EqualFold(o, s) }) {
			senders = append(senders, s)
		}
	}
	r.Senders = senders
	return r
}

func validateInbound(r Inbound) error {
	if len(r.Senders) > maxSenders {
		return invalid("inbound.senders: at most %d", maxSenders)
	}
	for _, s := range r.Senders {
		if len([]rune(s)) > maxSenderLen {
			return invalid("inbound.senders: %q is longer than %d characters", s, maxSenderLen)
		}
	}
	return nil
}

func inboundToProto(r Inbound) *v1.InboundRules {
	return &v1.InboundRules{Enabled: r.Enabled, Senders: r.Senders}
}

func inboundFromProto(m *v1.InboundRules) Inbound {
	return Inbound{Enabled: m.GetEnabled(), Senders: m.GetSenders()}
}
