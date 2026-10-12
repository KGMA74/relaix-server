// Package policy holds a device's rules — when it takes work and for which
// recipients — and decides, for one job and one device, whether and from which
// SIM it may be sent.
//
// The server is the only place they are enforced. The dashboard and the phone
// both edit them, but only as views of the copy stored here, and the scheduler
// asks Choose before handing any job to any device. Enforcing them on the phone
// as well would only add a second, possibly stale, opinion.
//
// Every field's empty value means "no restriction": the zero Policy is a phone
// that sends anything, any time, which is what every device was before rules
// existed.
package policy

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	// Zones from the binary, not the host: a runtime image without zoneinfo
	// would otherwise read every zone as UTC, without an error to notice.
	_ "time/tzdata"
)

// Policy is a device's rules.
type Policy struct {
	// Version is assigned by the store, +1 on every change.
	Version      int64        `json:"version"`
	Availability Availability `json:"availability"`
	Accept       AcceptRules  `json:"accept"`
	// Sims narrows the device's rules per SIM: a SIM must satisfy both.
	Sims      []SimPolicy `json:"sims,omitempty"`
	UpdatedAt *time.Time  `json:"updatedAt,omitempty"`
}

// Availability says when a device takes work.
type Availability struct {
	// TimeZone is the IANA zone the windows are read in. Empty: the zone the
	// phone reports, then UTC.
	TimeZone string `json:"timeZone,omitempty"`
	// Windows: available while inside any. None: always available.
	Windows []Window `json:"windows,omitempty"`
}

// Window is a daily slot.
type Window struct {
	// Days are ISO weekdays, 1 = Monday … 7 = Sunday. Empty: every day.
	Days []int `json:"days,omitempty"`
	// Start and End are minutes since midnight; End is exclusive. An End at or
	// before Start runs past midnight. Both 0 is the whole day.
	Start int `json:"start"`
	End   int `json:"end"`
}

// AcceptRules say which recipients are accepted. Both must hold.
type AcceptRules struct {
	// Countries are ISO 3166-1 alpha-2 codes. Empty: any country.
	Countries []string `json:"countries,omitempty"`
	// Pattern is an RE2 expression the E.164 recipient must match. Empty: any.
	Pattern string `json:"pattern,omitempty"`
}

// SimPolicy narrows the rules for one SIM.
type SimPolicy struct {
	SubscriptionID int32        `json:"subscriptionId"`
	Disabled       bool         `json:"disabled,omitempty"`
	Accept         AcceptRules  `json:"accept"`
	Availability   Availability `json:"availability"`
}

// maxPatternLen bounds a recipient pattern. RE2 runs in linear time, so this is
// not about catastrophic backtracking — only about keeping a rule readable and
// the stored row small.
const maxPatternLen = 500

// ErrInvalid wraps every validation failure, so callers can answer 400.
var ErrInvalid = errors.New("invalid policy")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Normalize returns p with country codes upper-cased and de-duplicated, days
// sorted and de-duplicated, and whitespace trimmed, so two equivalent policies
// are stored identically.
func Normalize(p Policy) Policy {
	p.Availability = normalizeAvailability(p.Availability)
	p.Accept = normalizeAccept(p.Accept)
	sims := make([]SimPolicy, 0, len(p.Sims))
	for _, s := range p.Sims {
		s.Accept = normalizeAccept(s.Accept)
		s.Availability = normalizeAvailability(s.Availability)
		sims = append(sims, s)
	}
	slices.SortFunc(sims, func(a, b SimPolicy) int { return int(a.SubscriptionID - b.SubscriptionID) })
	p.Sims = sims
	return p
}

func normalizeAvailability(a Availability) Availability {
	a.TimeZone = strings.TrimSpace(a.TimeZone)
	for i, w := range a.Windows {
		days := slices.Clone(w.Days)
		slices.Sort(days)
		a.Windows[i].Days = slices.Compact(days)
	}
	return a
}

func normalizeAccept(r AcceptRules) AcceptRules {
	var countries []string
	for _, c := range r.Countries {
		c = strings.ToUpper(strings.TrimSpace(c))
		if c != "" && !slices.Contains(countries, c) {
			countries = append(countries, c)
		}
	}
	slices.Sort(countries)
	r.Countries = countries
	r.Pattern = strings.TrimSpace(r.Pattern)
	return r
}

// Validate reports the first problem with p, wrapped in ErrInvalid.
func Validate(p Policy) error {
	if err := validateAvailability(p.Availability, "availability"); err != nil {
		return err
	}
	if err := validateAccept(p.Accept, "accept"); err != nil {
		return err
	}
	seen := map[int32]bool{}
	for i, s := range p.Sims {
		where := fmt.Sprintf("sims[%d]", i)
		if seen[s.SubscriptionID] {
			return invalid("%s: subscription %d listed twice", where, s.SubscriptionID)
		}
		seen[s.SubscriptionID] = true
		if err := validateAvailability(s.Availability, where+".availability"); err != nil {
			return err
		}
		if err := validateAccept(s.Accept, where+".accept"); err != nil {
			return err
		}
	}
	return nil
}

func validateAvailability(a Availability, where string) error {
	if a.TimeZone != "" {
		if _, err := time.LoadLocation(a.TimeZone); err != nil {
			return invalid("%s.timeZone: unknown zone %q", where, a.TimeZone)
		}
	}
	for i, w := range a.Windows {
		if w.Start < 0 || w.Start >= 24*60 || w.End < 0 || w.End > 24*60 {
			return invalid("%s.windows[%d]: start and end are minutes in [0, 1440]", where, i)
		}
		for _, d := range w.Days {
			if d < 1 || d > 7 {
				return invalid("%s.windows[%d]: day %d is not an ISO weekday (1-7)", where, i, d)
			}
		}
	}
	return nil
}

func validateAccept(r AcceptRules, where string) error {
	for _, c := range r.Countries {
		if !KnownCountry(c) {
			return invalid("%s.countries: unknown country %q", where, c)
		}
	}
	if len(r.Pattern) > maxPatternLen {
		return invalid("%s.pattern: longer than %d characters", where, maxPatternLen)
	}
	if r.Pattern != "" {
		if _, err := regexp.Compile(r.Pattern); err != nil {
			return invalid("%s.pattern: %v", where, err)
		}
	}
	return nil
}

// Open reports whether now falls inside the availability. fallbackZone is the
// zone the phone reports, used when the availability names none; an unknown
// zone reads as UTC rather than failing the whole decision.
func (a Availability) Open(now time.Time, fallbackZone string) bool {
	if len(a.Windows) == 0 {
		return true
	}
	loc := location(a.TimeZone, fallbackZone)
	t := now.In(loc)
	minute := t.Hour()*60 + t.Minute()
	today := isoWeekday(t.Weekday())
	yesterday := today - 1
	if yesterday == 0 {
		yesterday = 7
	}
	for _, w := range a.Windows {
		switch {
		case w.Start == 0 && w.End == 0:
			if w.onDay(today) {
				return true
			}
		case w.End > w.Start:
			if w.onDay(today) && minute >= w.Start && minute < w.End {
				return true
			}
		default:
			// Past midnight: the evening part belongs to the day the window
			// starts on, the early-morning part to the day before.
			if w.onDay(today) && minute >= w.Start {
				return true
			}
			if w.onDay(yesterday) && minute < w.End {
				return true
			}
		}
	}
	return false
}

func (w Window) onDay(day int) bool {
	return len(w.Days) == 0 || slices.Contains(w.Days, day)
}

func isoWeekday(d time.Weekday) int {
	if d == time.Sunday {
		return 7
	}
	return int(d)
}

func location(zone, fallback string) *time.Location {
	for _, z := range []string{zone, fallback} {
		if z == "" {
			continue
		}
		if loc, err := time.LoadLocation(z); err == nil {
			return loc
		}
	}
	return time.UTC
}

// patterns caches compiled recipient patterns: the scheduler evaluates them for
// every job against every device on every tick, and a fleet has few distinct
// patterns.
var patterns sync.Map // string -> *regexp.Regexp (nil when invalid)

func compiled(pattern string) *regexp.Regexp {
	if re, ok := patterns.Load(pattern); ok {
		return re.(*regexp.Regexp)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		re = nil
	}
	patterns.Store(pattern, re)
	return re
}

// Accepts reports whether recipient satisfies the rules. A stored pattern that
// no longer compiles accepts nothing: refusing is the safe reading of a rule
// that cannot be evaluated.
func (r AcceptRules) Accepts(recipient string) bool {
	if len(r.Countries) > 0 && !slices.ContainsFunc(r.Countries, func(c string) bool { return inCountry(recipient, c) }) {
		return false
	}
	if r.Pattern != "" {
		re := compiled(r.Pattern)
		if re == nil || !re.MatchString(recipient) {
			return false
		}
	}
	return true
}

func (p Policy) sim(subscriptionID int32) *SimPolicy {
	for i := range p.Sims {
		if p.Sims[i].SubscriptionID == subscriptionID {
			return &p.Sims[i]
		}
	}
	return nil
}

// Choice is the outcome of Choose.
type Choice struct {
	// OK: the job may go to this device now.
	OK bool
	// SubscriptionID is the SIM to send from. Zero means the phone's default
	// SMS subscription, chosen when no SIM rule is involved.
	SubscriptionID int32
	// Never: no SIM of this device will ever accept the recipient, whatever
	// the time. Distinct from "not now", which is worth waiting for.
	Never bool
}

// Device is what Choose needs to know about a device besides its policy.
type Device struct {
	// TimeZone is the zone the phone reports.
	TimeZone string
	// Sims are its subscription ids, the default one first. Empty when the
	// phone has not reported them.
	Sims []int32
}

// Choose decides whether a job for recipient may go to a device now, and from
// which SIM. requested is the SIM the job names, or 0.
//
// Never is decided first and without regard to the time: a recipient the
// device's rules refuse, or that every candidate SIM refuses or is disabled
// for. Only then does the clock matter: the device's windows, and the SIM's.
// Candidates are the requested SIM alone if the job names one, otherwise every
// SIM with the default first; the first that may send now wins.
func (p Policy) Choose(now time.Time, recipient string, d Device, requested int32) Choice {
	if !p.Accept.Accepts(recipient) {
		return Choice{Never: true}
	}
	deviceOpen := p.Availability.Open(now, d.TimeZone)

	candidates := d.Sims
	if requested != 0 {
		candidates = []int32{requested}
	}
	// No SIM rules, or no SIM inventory to apply them to: the job keeps
	// whatever SIM it named, or the phone's default.
	if len(p.Sims) == 0 || len(candidates) == 0 {
		return Choice{OK: deviceOpen, SubscriptionID: requested}
	}

	never := true
	for _, sub := range candidates {
		sp := p.sim(sub)
		if sp != nil && (sp.Disabled || !sp.Accept.Accepts(recipient)) {
			continue
		}
		never = false
		if deviceOpen && (sp == nil || sp.Availability.Open(now, d.TimeZone)) {
			return Choice{OK: true, SubscriptionID: sub}
		}
	}
	return Choice{Never: never}
}

// AcceptsEver reports whether the device could ever take a job for recipient,
// whatever the time — the question behind failing a job that no device in the
// fleet will accept, instead of leaving it to wait for ever.
func (p Policy) AcceptsEver(recipient string, d Device, requested int32) bool {
	// Never does not depend on the instant, so any one will do.
	return !p.Choose(time.Time{}, recipient, d, requested).Never
}
