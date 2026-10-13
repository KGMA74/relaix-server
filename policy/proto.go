package policy

import (
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/KGMA74/relaix-server/gen/smsgateway/v1"
)

// ToProto converts p for the wire.
func ToProto(p Policy) *v1.DevicePolicy {
	out := &v1.DevicePolicy{
		Version:      p.Version,
		Availability: availabilityToProto(p.Availability),
		Accept:       acceptToProto(p.Accept),
		Limits:       limitsToProto(p.Limits),
		Inbound:      inboundToProto(p.Inbound),
	}
	if p.UpdatedAt != nil {
		out.UpdatedAt = timestamppb.New(*p.UpdatedAt)
	}
	for _, s := range p.Sims {
		out.Sims = append(out.Sims, &v1.SimPolicy{
			SubscriptionId: s.SubscriptionID,
			Disabled:       s.Disabled,
			Accept:         acceptToProto(s.Accept),
			Availability:   availabilityToProto(s.Availability),
			Limits:         limitsToProto(s.Limits),
		})
	}
	return out
}

// FromProto converts a policy received from a device. Version and UpdatedAt
// are not taken from it: the store assigns both.
func FromProto(m *v1.DevicePolicy) Policy {
	p := Policy{
		Availability: availabilityFromProto(m.GetAvailability()),
		Accept:       acceptFromProto(m.GetAccept()),
		Limits:       limitsFromProto(m.GetLimits()),
		Inbound:      inboundFromProto(m.GetInbound()),
	}
	for _, s := range m.GetSims() {
		p.Sims = append(p.Sims, SimPolicy{
			SubscriptionID: s.GetSubscriptionId(),
			Disabled:       s.GetDisabled(),
			Accept:         acceptFromProto(s.GetAccept()),
			Availability:   availabilityFromProto(s.GetAvailability()),
			Limits:         limitsFromProto(s.GetLimits()),
		})
	}
	return p
}

func availabilityToProto(a Availability) *v1.Availability {
	out := &v1.Availability{TimeZone: a.TimeZone}
	for _, w := range a.Windows {
		days := make([]int32, len(w.Days))
		for i, d := range w.Days {
			days[i] = int32(d)
		}
		out.Windows = append(out.Windows, &v1.TimeWindow{
			Days:        days,
			StartMinute: int32(w.Start),
			EndMinute:   int32(w.End),
		})
	}
	return out
}

func availabilityFromProto(m *v1.Availability) Availability {
	a := Availability{TimeZone: m.GetTimeZone()}
	for _, w := range m.GetWindows() {
		var days []int
		for _, d := range w.GetDays() {
			days = append(days, int(d))
		}
		a.Windows = append(a.Windows, Window{
			Days:  days,
			Start: int(w.GetStartMinute()),
			End:   int(w.GetEndMinute()),
		})
	}
	return a
}

func acceptToProto(r AcceptRules) *v1.AcceptRules {
	return &v1.AcceptRules{Countries: r.Countries, RecipientPattern: r.Pattern}
}

func acceptFromProto(m *v1.AcceptRules) AcceptRules {
	return AcceptRules{Countries: m.GetCountries(), Pattern: m.GetRecipientPattern()}
}

func limitsToProto(l Limits) *v1.SendLimits {
	return &v1.SendLimits{PerHour: int32(l.PerHour), PerDay: int32(l.PerDay)}
}

func limitsFromProto(m *v1.SendLimits) Limits {
	return Limits{PerHour: int(m.GetPerHour()), PerDay: int(m.GetPerDay())}
}
