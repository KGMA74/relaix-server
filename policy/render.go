package policy

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// IsJSONTemplate tells a webhook body meant as JSON — an object or an array,
// as the receiver will parse it — from plain text. "{amount}" alone is a
// field, not an object: an object opens on a quote or closes at once.
func IsJSONTemplate(template string) bool {
	t := strings.TrimLeft(template, " \t\r\n")
	if strings.HasPrefix(t, "[") {
		return true
	}
	if !strings.HasPrefix(t, "{") {
		return false
	}
	rest := strings.TrimLeft(t[1:], " \t\r\n")
	return strings.HasPrefix(rest, `"`) || strings.HasPrefix(rest, "}")
}

// RenderJSON fills a JSON template so that it stays JSON whatever the SMS
// said. A field inside a string literal is escaped there: a quote or a line
// break in the message cannot end the string. A field outside one is a
// value: a number when it reads as one ("5 000" gives 5000), null when
// empty, otherwise a string — so {"amount": {amount}} hands the receiver a
// number it does not have to parse, and a phone number left unquoted by
// mistake still arrives whole.
//
// The agent and the dashboard implement the same rules; the shared vectors
// (testdata/automation_vectors.json) hold all three to them.
func RenderJSON(template string, fields map[string]string) string {
	var b strings.Builder
	inString := false
	for i := 0; i < len(template); {
		c := template[i]
		switch {
		case inString && c == '\\' && i+1 < len(template):
			b.WriteString(template[i : i+2])
			i += 2
			continue
		case c == '"':
			inString = !inString
		case c == '{':
			if m := placeholderAt.FindStringSubmatchIndex(template[i:]); m != nil {
				v := fields[template[i+m[2]:i+m[3]]]
				if inString {
					b.WriteString(EscapeJSON(v))
				} else {
					b.WriteString(JSONValue(v))
				}
				i += m[1]
				continue
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// JSONValue is a field as a JSON value of its own: see RenderJSON.
func JSONValue(v string) string {
	if v == "" {
		return "null"
	}
	if n, ok := JSONNumber(v); ok {
		return n
	}
	return `"` + EscapeJSON(v) + `"`
}

// JSONNumber is v as a JSON number literal, when it is one once read the way
// SMS write amounts (spaces dropped, a comma for the decimal point). Not when
// it starts with + or a leading zero: that is a phone number or a reference,
// and a number would lose what makes it one.
func JSONNumber(v string) (string, bool) {
	s := strings.Map(func(r rune) rune {
		switch r {
		case ' ', ' ', ' ':
			return -1
		case ',':
			return '.'
		}
		return r
	}, v)
	if !jsonNumber.MatchString(s) {
		return "", false
	}
	return s, true
}

// Numbers is the fields that read as numbers, as JSON literals, for payloads
// that carry fields as text: "numbers": {"amount": 5000} next to "fields".
// Builtins are left out; sim and the like are not amounts.
func Numbers(fields map[string]string) map[string]string {
	var out map[string]string
	for k, v := range fields {
		if isBuiltin(k) {
			continue
		}
		if n, ok := JSONNumber(v); ok {
			if out == nil {
				out = map[string]string{}
			}
			out[k] = n
		}
	}
	return out
}

// EscapeJSON is v as the inside of a JSON string literal. Spelled out rather
// than taken from encoding/json, which also escapes <, > and &: the agent and
// the dashboard escape exactly this, and the three must write the same bytes.
func EscapeJSON(v string) string {
	var b strings.Builder
	for _, r := range v {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

func isBuiltin(name string) bool { return slices.Contains(Builtins, name) }

var (
	placeholderAt = regexp.MustCompile(`^\{([A-Za-z][A-Za-z0-9]*)\}`)
	jsonNumber    = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)
)
