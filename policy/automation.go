package policy

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"

	v1 "github.com/KGMA74/relaix-server/gen/smsgateway/v1"
)

// Automation is what a phone does by itself when an SMS arrives: match it,
// extract fields, act. Defined here, run on the phone (protocol.md §11); the
// gateway validates it so that whatever is stored runs the same there.
type Automation struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Description says what it is for, in a sentence or two: for the people
	// reading the list, and for an assistant choosing among automations.
	Description string `json:"description,omitempty"`
	Enabled     bool   `json:"enabled,omitempty"`
	// SubscriptionID is the SIM it listens on; 0: any.
	SubscriptionID int32 `json:"subscriptionId,omitempty"`
	// Senders are matched whole, ignoring case. Empty: any sender.
	Senders []string `json:"senders,omitempty"`
	// Pattern must be found in the body; its named groups are the fields.
	Pattern    string      `json:"pattern,omitempty"`
	Conditions []Condition `json:"conditions,omitempty"`
	Actions    []Action    `json:"actions"`
	// Limits cap runs per hour and day, counted on the phone.
	Limits Limits `json:"limits"`
	// Schedule, when set, runs the automation by itself at intervals rather
	// than on received SMS; it then has no senders and no pattern.
	Schedule *Schedule `json:"schedule,omitempty"`
}

// Schedule runs an automation every EveryMinutes, inside Window.
type Schedule struct {
	EveryMinutes int          `json:"everyMinutes"`
	Window       Availability `json:"window"`
}

// Paging follows a USSD answer over several screens: while the last one
// contains Marker ("Suivant"), Reply ("n") is typed for the next, up to
// MaxPages screens.
type Paging struct {
	Marker   string `json:"marker"`
	Reply    string `json:"reply"`
	MaxPages int    `json:"maxPages,omitempty"`
}

// Condition tests one field.
type Condition struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

// Action is one step. Type picks which of the other fields apply.
type Action struct {
	Type   string `json:"type"`
	To     string `json:"to,omitempty"`
	Text   string `json:"text,omitempty"`
	URL    string `json:"url,omitempty"`
	Body   string `json:"body,omitempty"`
	Secret string `json:"secret,omitempty"`
	Code   string `json:"code,omitempty"`
	// A USSD action: menus answered, screens followed, and a pattern whose
	// named groups, found in what the network displayed, become fields for
	// the actions after it.
	Replies []string `json:"replies,omitempty"`
	Paging  *Paging  `json:"paging,omitempty"`
	Extract string   `json:"extract,omitempty"`
}

// Action types.
const (
	ActionForward  = "forward"
	ActionReplySMS = "reply_sms"
	ActionWebhook  = "webhook"
	ActionUSSD     = "ussd"
)

// Condition operators.
var (
	textOps    = []string{"eq", "ne", "contains"}
	numericOps = []string{"gt", "gte", "lt", "lte"}
)

// Builtins are the fields every automation run on a received SMS has,
// pattern or not.
var Builtins = []string{"sender", "body", "receivedAt", "sim"}

// ScheduledBuiltins are those of an automation run on a schedule.
var ScheduledBuiltins = []string{"now", "sim"}

// PinField is the placeholder for the SIM's PIN, resolved on the phone, and
// only in a USSD action.
const PinField = "pin"

const (
	maxAutomations    = 20
	maxActions        = 5
	maxConditions     = 10
	maxNameLen        = 64
	maxDescriptionLen = 500
	maxReplyLen       = 480
	maxWebhookBodyLen = 4000
	maxSecretLen      = 128
	maxReplies        = 10
	maxReplyText      = 64
	maxPages          = 10
	defaultPages      = 3
	minScheduleMins   = 15
	maxScheduleMins   = 7 * 24 * 60
)

var (
	placeholder = regexp.MustCompile(`\{([A-Za-z][A-Za-z0-9]*)\}`)
	// groupName finds named groups in either spelling RE2 accepts.
	groupName = regexp.MustCompile(`\(\?P?<([^>]*)>`)
	fieldName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)
)

// Fields lists what an automation's templates and conditions may use: the
// builtins, then the pattern's named groups.
func (a Automation) Fields() []string {
	if a.Schedule != nil {
		return slices.Clone(ScheduledBuiltins)
	}
	return appendGroups(slices.Clone(Builtins), a.Pattern)
}

// FieldsAt is what action i may use: Fields, and the groups the USSD
// actions before it extract.
func (a Automation) FieldsAt(i int) []string {
	out := a.Fields()
	for _, act := range a.Actions[:min(i, len(a.Actions))] {
		if act.Type == ActionUSSD {
			out = appendGroups(out, act.Extract)
		}
	}
	return out
}

func appendGroups(out []string, pattern string) []string {
	for _, m := range groupName.FindAllStringSubmatch(pattern, -1) {
		if !slices.Contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	return out
}

// Extract searches what a USSD code displayed — every screen joined by a
// line break — and returns the pattern's named groups. Nothing found, or no
// pattern: an empty map and false. It is the reference the phone mirrors.
func Extract(pattern string, screens []string) (map[string]string, bool) {
	if pattern == "" {
		return map[string]string{}, false
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return map[string]string{}, false
	}
	m := re.FindStringSubmatch(strings.Join(screens, "\n"))
	if m == nil {
		return map[string]string{}, false
	}
	out := map[string]string{}
	for i, name := range re.SubexpNames() {
		if name != "" {
			out[name] = strings.TrimSpace(m[i])
		}
	}
	return out, true
}

// Match reports whether a message triggers the automation, and the fields it
// yields. It is the reference the phone and the dashboard's tester mirror.
func (a Automation) Match(sender, body, receivedAt string, sim int32) (map[string]string, bool) {
	if !a.Enabled || a.Schedule != nil || (a.SubscriptionID != 0 && a.SubscriptionID != sim) {
		return nil, false
	}
	if len(a.Senders) > 0 && !slices.ContainsFunc(a.Senders, func(s string) bool {
		return strings.EqualFold(s, strings.TrimSpace(sender))
	}) {
		return nil, false
	}
	fields := map[string]string{
		"sender": sender, "body": body, "receivedAt": receivedAt, "sim": strconv.Itoa(int(sim)),
	}
	if a.Pattern != "" {
		re, err := regexp.Compile(a.Pattern)
		if err != nil {
			return nil, false
		}
		m := re.FindStringSubmatch(body)
		if m == nil {
			return nil, false
		}
		for i, name := range re.SubexpNames() {
			if name != "" {
				fields[name] = m[i]
			}
		}
	}
	for _, c := range a.Conditions {
		if !c.holds(fields[c.Field]) {
			return nil, false
		}
	}
	return fields, true
}

func (c Condition) holds(v string) bool {
	switch c.Op {
	case "eq":
		return strings.EqualFold(v, c.Value)
	case "ne":
		return !strings.EqualFold(v, c.Value)
	case "contains":
		return strings.Contains(strings.ToLower(v), strings.ToLower(c.Value))
	}
	got, ok1 := Number(v)
	want, ok2 := Number(c.Value)
	if !ok1 || !ok2 {
		return false
	}
	switch c.Op {
	case "gt":
		return got > want
	case "gte":
		return got >= want
	case "lt":
		return got < want
	case "lte":
		return got <= want
	}
	return false
}

// Number reads an amount as SMS write them: "5 000", "5000,50", "5.000".
// Spaces (including non-breaking ones) are dropped and a comma is a decimal
// point.
func Number(s string) (float64, bool) {
	s = strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\u00a0', '\u202f':
			return -1
		case ',':
			return '.'
		}
		return r
	}, s)
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}

// Render replaces each {name} with its field. A name without a field renders
// empty: validation already refused unknown names.
func Render(template string, fields map[string]string) string {
	return placeholder.ReplaceAllStringFunc(template, func(m string) string {
		return fields[m[1:len(m)-1]]
	})
}

func normalizeAutomations(in []Automation) []Automation {
	// A copy all the way down: Normalize must not edit the caller's rules.
	list := slices.Clone(in)
	for i := range list {
		a := &list[i]
		a.Conditions = slices.Clone(a.Conditions)
		a.Actions = slices.Clone(a.Actions)
		a.ID = strings.TrimSpace(a.ID)
		if a.ID == "" {
			a.ID = uuid.NewString()
		}
		a.Name = strings.TrimSpace(a.Name)
		a.Description = strings.TrimSpace(a.Description)
		a.Pattern = strings.TrimSpace(a.Pattern)
		a.Senders = normalizeInbound(Inbound{Senders: a.Senders}).Senders
		for j := range a.Conditions {
			a.Conditions[j].Field = strings.TrimSpace(a.Conditions[j].Field)
			a.Conditions[j].Op = strings.TrimSpace(a.Conditions[j].Op)
		}
		for j := range a.Actions {
			act := &a.Actions[j]
			act.Type = strings.TrimSpace(act.Type)
			act.To = strings.TrimSpace(act.To)
			act.URL = strings.TrimSpace(act.URL)
			act.Code = strings.TrimSpace(act.Code)
			act.Extract = strings.TrimSpace(act.Extract)
			act.Replies = slices.Clone(act.Replies)
			for k := range act.Replies {
				act.Replies[k] = strings.TrimSpace(act.Replies[k])
			}
			if act.Paging != nil {
				p := *act.Paging
				p.Marker, p.Reply = strings.TrimSpace(p.Marker), strings.TrimSpace(p.Reply)
				act.Paging = &p
				if p.Marker == "" && p.Reply == "" {
					act.Paging = nil
				}
			}
		}
		if a.Schedule != nil {
			sch := *a.Schedule
			sch.Window = normalizeAvailability(sch.Window)
			a.Schedule = &sch
		}
	}
	return list
}

func validateAutomations(list []Automation) error {
	if len(list) > maxAutomations {
		return invalid("automations: at most %d per device", maxAutomations)
	}
	seen := map[string]bool{}
	for i, a := range list {
		where := fmt.Sprintf("automations[%d]", i)
		if seen[a.ID] {
			return invalid("%s: id %q used twice", where, a.ID)
		}
		seen[a.ID] = true
		if err := a.validate(where); err != nil {
			return err
		}
	}
	return nil
}

func (a Automation) validate(where string) error {
	if a.Name == "" || len([]rune(a.Name)) > maxNameLen {
		return invalid("%s.name: 1 to %d characters", where, maxNameLen)
	}
	if len([]rune(a.Description)) > maxDescriptionLen {
		return invalid("%s.description: at most %d characters", where, maxDescriptionLen)
	}
	if a.SubscriptionID < 0 {
		return invalid("%s.subscriptionId: not a subscription", where)
	}
	if err := validateInbound(Inbound{Senders: a.Senders}); err != nil {
		return invalid("%s: %v", where, err)
	}
	if a.Schedule != nil {
		if len(a.Senders) > 0 || a.Pattern != "" {
			return invalid("%s: a scheduled automation has no senders and no pattern: no SMS starts it", where)
		}
		if a.Schedule.EveryMinutes < minScheduleMins || a.Schedule.EveryMinutes > maxScheduleMins {
			return invalid("%s.schedule.everyMinutes: from %d (Android's shortest period) to %d", where, minScheduleMins, maxScheduleMins)
		}
		if err := validateAvailability(a.Schedule.Window, where+".schedule.window"); err != nil {
			return err
		}
	}
	if err := validatePattern(a.Pattern, where+".pattern", Builtins); err != nil {
		return err
	}
	fields := a.Fields()
	if len(a.Conditions) > maxConditions {
		return invalid("%s.conditions: at most %d", where, maxConditions)
	}
	for j, c := range a.Conditions {
		cw := fmt.Sprintf("%s.conditions[%d]", where, j)
		if !slices.Contains(fields, c.Field) {
			return invalid("%s.field: %q is not a field of this automation", cw, c.Field)
		}
		switch {
		case slices.Contains(textOps, c.Op):
		case slices.Contains(numericOps, c.Op):
			if _, ok := Number(c.Value); !ok {
				return invalid("%s.value: %q is not a number", cw, c.Value)
			}
		default:
			return invalid("%s.op: one of eq, ne, contains, gt, gte, lt, lte", cw)
		}
	}
	if len(a.Actions) == 0 || len(a.Actions) > maxActions {
		return invalid("%s.actions: 1 to %d", where, maxActions)
	}
	for j, act := range a.Actions {
		aw := fmt.Sprintf("%s.actions[%d]", where, j)
		known := a.FieldsAt(j)
		if err := act.validate(aw, known); err != nil {
			return err
		}
		if act.Type == ActionUSSD && act.Extract != "" {
			// What it extracts must not shadow a field the run already has.
			if err := validatePattern(act.Extract, aw+".extract", known); err != nil {
				return err
			}
		}
	}
	return validateLimits(a.Limits, where+".limits")
}

// validatePattern checks a regular expression whose named groups become
// fields: it compiles, and each name is a letter then letters or digits, not
// one of taken nor {pin}.
func validatePattern(pattern, where string, taken []string) error {
	if pattern == "" {
		return nil
	}
	if len(pattern) > maxPatternLen {
		return invalid("%s: longer than %d characters", where, maxPatternLen)
	}
	if _, err := regexp.Compile(pattern); err != nil {
		return invalid("%s: %v", where, err)
	}
	for _, m := range groupName.FindAllStringSubmatch(pattern, -1) {
		if !fieldName.MatchString(m[1]) || slices.Contains(taken, m[1]) || m[1] == PinField {
			return invalid("%s: group name %q must be a letter then letters or digits, and not %s or %s",
				where, m[1], strings.Join(taken, ", "), PinField)
		}
	}
	return nil
}

func (act Action) validate(where string, fields []string) error {
	uses := func(name, template string, pin bool) error {
		for _, m := range placeholder.FindAllStringSubmatch(template, -1) {
			if m[1] == PinField && pin {
				continue
			}
			if !slices.Contains(fields, m[1]) {
				return invalid("%s.%s: {%s} is not a field of this automation", where, name, m[1])
			}
		}
		return nil
	}
	switch act.Type {
	case ActionForward:
		return nil
	case ActionReplySMS:
		if act.To == "" || act.Text == "" || len([]rune(act.Text)) > maxReplyLen {
			return invalid("%s: reply_sms needs to and a text of at most %d characters", where, maxReplyLen)
		}
		if !placeholder.MatchString(act.To) && !e164.MatchString(act.To) {
			return invalid("%s.to: an E.164 number or a field, e.g. {sender}", where)
		}
		if err := uses("to", act.To, false); err != nil {
			return err
		}
		return uses("text", act.Text, false)
	case ActionWebhook:
		u, err := url.Parse(act.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return invalid("%s.url: an absolute http or https URL", where)
		}
		if len(act.Body) > maxWebhookBodyLen || len(act.Secret) > maxSecretLen {
			return invalid("%s: body at most %d characters, secret at most %d", where, maxWebhookBodyLen, maxSecretLen)
		}
		if err := uses("body", act.Body, false); err != nil {
			return err
		}
		// A body meant as JSON must still be JSON once filled in: checked
		// here, with every field set, rather than found broken by the
		// receiver on the first payment.
		if IsJSONTemplate(act.Body) {
			sample := map[string]string{}
			for _, f := range fields {
				sample[f] = "0"
			}
			if !json.Valid([]byte(RenderJSON(act.Body, sample))) {
				return invalid(`%s.body: not valid JSON once filled in — quote a field for text ("{from}"), leave it bare for a number ({amount})`, where)
			}
		}
		return nil
	case ActionUSSD:
		if !ussdTemplate.MatchString(act.Code) {
			return invalid("%s.code: a USSD code, fields allowed: *144*2*1*{from}*{amount}*{pin}#", where)
		}
		if len(act.Replies) > maxReplies {
			return invalid("%s.replies: at most %d", where, maxReplies)
		}
		for k, r := range act.Replies {
			if r == "" || len([]rune(r)) > maxReplyText {
				return invalid("%s.replies[%d]: 1 to %d characters", where, k, maxReplyText)
			}
			if err := uses(fmt.Sprintf("replies[%d]", k), r, true); err != nil {
				return err
			}
		}
		if p := act.Paging; p != nil {
			if p.Marker == "" || len([]rune(p.Marker)) > 40 || p.Reply == "" || len([]rune(p.Reply)) > 8 {
				return invalid("%s.paging: a marker (e.g. Suivant, 40 characters at most) and the reply that shows the next screen (e.g. n)", where)
			}
			if p.MaxPages < 0 || p.MaxPages > maxPages {
				return invalid("%s.paging.maxPages: 0 (%d) to %d", where, defaultPages, maxPages)
			}
		}
		return uses("code", act.Code, true)
	}
	return invalid("%s.type: one of forward, reply_sms, webhook, ussd", where)
}

var (
	e164         = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)
	ussdTemplate = regexp.MustCompile(`^[*#]([0-9*#+]|\{[A-Za-z][A-Za-z0-9]*\})*#$`)
)

func automationsToProto(list []Automation) []*v1.Automation {
	out := make([]*v1.Automation, 0, len(list))
	for _, a := range list {
		m := &v1.Automation{
			Id: a.ID, Name: a.Name, Description: a.Description, Enabled: a.Enabled, SubscriptionId: a.SubscriptionID,
			Senders: a.Senders, Pattern: a.Pattern, Limits: limitsToProto(a.Limits),
		}
		if a.Schedule != nil {
			m.Schedule = &v1.AutomationSchedule{EveryMinutes: int32(a.Schedule.EveryMinutes), Window: availabilityToProto(a.Schedule.Window)}
		}
		for _, c := range a.Conditions {
			m.Conditions = append(m.Conditions, &v1.AutomationCondition{Field: c.Field, Op: c.Op, Value: c.Value})
		}
		for _, act := range a.Actions {
			pa := &v1.AutomationAction{}
			switch act.Type {
			case ActionForward:
				pa.Action = &v1.AutomationAction_Forward{Forward: &v1.ForwardAction{}}
			case ActionReplySMS:
				pa.Action = &v1.AutomationAction_ReplySms{ReplySms: &v1.ReplySmsAction{To: act.To, Text: act.Text}}
			case ActionWebhook:
				pa.Action = &v1.AutomationAction_Webhook{Webhook: &v1.WebhookAction{Url: act.URL, Body: act.Body, Secret: act.Secret}}
			case ActionUSSD:
				pa.Action = &v1.AutomationAction_Ussd{Ussd: &v1.UssdAction{
					Code: act.Code, Replies: act.Replies, Paging: PagingToProto(act.Paging), Extract: act.Extract,
				}}
			}
			m.Actions = append(m.Actions, pa)
		}
		out = append(out, m)
	}
	return out
}

func automationsFromProto(list []*v1.Automation) []Automation {
	var out []Automation
	for _, m := range list {
		a := Automation{
			ID: m.GetId(), Name: m.GetName(), Description: m.GetDescription(), Enabled: m.GetEnabled(), SubscriptionID: m.GetSubscriptionId(),
			Senders: m.GetSenders(), Pattern: m.GetPattern(), Limits: limitsFromProto(m.GetLimits()),
		}
		if sch := m.GetSchedule(); sch != nil {
			a.Schedule = &Schedule{EveryMinutes: int(sch.GetEveryMinutes()), Window: availabilityFromProto(sch.GetWindow())}
		}
		for _, c := range m.GetConditions() {
			a.Conditions = append(a.Conditions, Condition{Field: c.GetField(), Op: c.GetOp(), Value: c.GetValue()})
		}
		for _, pa := range m.GetActions() {
			switch x := pa.GetAction().(type) {
			case *v1.AutomationAction_Forward:
				a.Actions = append(a.Actions, Action{Type: ActionForward})
			case *v1.AutomationAction_ReplySms:
				a.Actions = append(a.Actions, Action{Type: ActionReplySMS, To: x.ReplySms.GetTo(), Text: x.ReplySms.GetText()})
			case *v1.AutomationAction_Webhook:
				a.Actions = append(a.Actions, Action{Type: ActionWebhook, URL: x.Webhook.GetUrl(), Body: x.Webhook.GetBody(), Secret: x.Webhook.GetSecret()})
			case *v1.AutomationAction_Ussd:
				a.Actions = append(a.Actions, Action{
					Type: ActionUSSD, Code: x.Ussd.GetCode(), Replies: x.Ussd.GetReplies(),
					Paging: pagingFromProto(x.Ussd.GetPaging()), Extract: x.Ussd.GetExtract(),
				})
			}
		}
		out = append(out, a)
	}
	return out
}

// Forwards reports whether one of the automation's actions hands the SMS to
// the gateway.
func (a Automation) Forwards() bool {
	return slices.ContainsFunc(a.Actions, func(act Action) bool { return act.Type == ActionForward })
}

// Automation returns the device's automation with that id.
func (p Policy) Automation(id string) (Automation, bool) {
	for _, a := range p.Automations {
		if a.ID == id {
			return a, true
		}
	}
	return Automation{}, false
}

// PagingToProto is p on the wire; nil stays nil.
func PagingToProto(p *Paging) *v1.UssdPaging {
	if p == nil {
		return nil
	}
	return &v1.UssdPaging{Marker: p.Marker, Reply: p.Reply, MaxPages: int32(p.MaxPages)}
}

func pagingFromProto(m *v1.UssdPaging) *Paging {
	if m == nil || (m.GetMarker() == "" && m.GetReply() == "") {
		return nil
	}
	return &Paging{Marker: m.GetMarker(), Reply: m.GetReply(), MaxPages: int(m.GetMaxPages())}
}

// ValidatePaging checks paging as a USSD job carries it.
func ValidatePaging(p *Paging) error {
	if p == nil {
		return nil
	}
	return Action{Type: ActionUSSD, Code: "*1#", Paging: p}.validate("paging", nil)
}

// ValidateExtract checks an extraction pattern as a USSD job carries it.
func ValidateExtract(pattern string) error {
	return validatePattern(pattern, "extract", nil)
}
