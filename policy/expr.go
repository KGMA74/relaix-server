package policy

import (
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Arithmetic in templates: {amount * 2}, {amount - 100}, {(amount + fee) * 0.98}.
// Fields read as amounts (Number), + - * / and parentheses, nothing else.
// The result is rounded to two decimals in text and JSON, to a whole number
// in a USSD code (RenderUSSD); an expression that cannot be computed — a
// field that is not a number, a division by zero — renders empty in text,
// null in JSON, and makes a USSD code undialable.
//
// The agent (AutomationRules) and the dashboard (lib/automation.ts) parse and
// round the same way; the shared vectors hold the three to it.

var (
	// token is what a template fills between braces: a field name, or an
	// expression. Anything else in braces ({1}, { }) is left as written.
	token   = regexp.MustCompile(`\{([A-Za-z][A-Za-z0-9]*|[A-Za-z0-9. ()+*/-]+)\}`)
	tokenAt = regexp.MustCompile(`^\{([A-Za-z][A-Za-z0-9]*|[A-Za-z0-9. ()+*/-]+)\}`)
)

const maxExprLen = 100

// isExpression tells an expression from a field name or a literal in braces.
func isExpression(inner string) bool {
	return !fieldName.MatchString(inner) && strings.ContainsAny(inner, "+-*/")
}

// expr is a parsed expression, evaluated against a run's fields.
type expr func(get func(string) (float64, error)) (float64, error)

var (
	errNotNumber  = errors.New("not a number")
	errDivByZero  = errors.New("division by zero")
	errExprSyntax = errors.New("invalid expression")
)

// parseExpr parses s and lists the fields it uses.
func parseExpr(s string) (expr, []string, error) {
	if len(s) > maxExprLen {
		return nil, nil, errExprSyntax
	}
	p := &exprParser{s: s}
	e := p.sum()
	p.space()
	if p.err != nil || p.i != len(p.s) {
		return nil, nil, errExprSyntax
	}
	return e, p.names, nil
}

type exprParser struct {
	s     string
	i     int
	names []string
	err   error
}

func (p *exprParser) space() {
	for p.i < len(p.s) && p.s[p.i] == ' ' {
		p.i++
	}
}

func (p *exprParser) peek() byte {
	p.space()
	if p.i < len(p.s) {
		return p.s[p.i]
	}
	return 0
}

func (p *exprParser) sum() expr {
	left := p.product()
	for p.err == nil {
		op := p.peek()
		if op != '+' && op != '-' {
			return left
		}
		p.i++
		l, r := left, p.product()
		left = func(get func(string) (float64, error)) (float64, error) {
			a, err := l(get)
			if err != nil {
				return 0, err
			}
			b, err := r(get)
			if err != nil {
				return 0, err
			}
			if op == '+' {
				return a + b, nil
			}
			return a - b, nil
		}
	}
	return left
}

func (p *exprParser) product() expr {
	left := p.unary()
	for p.err == nil {
		op := p.peek()
		if op != '*' && op != '/' {
			return left
		}
		p.i++
		l, r := left, p.unary()
		left = func(get func(string) (float64, error)) (float64, error) {
			a, err := l(get)
			if err != nil {
				return 0, err
			}
			b, err := r(get)
			if err != nil {
				return 0, err
			}
			if op == '*' {
				return a * b, nil
			}
			if b == 0 {
				return 0, errDivByZero
			}
			return a / b, nil
		}
	}
	return left
}

func (p *exprParser) unary() expr {
	if p.peek() == '-' {
		p.i++
		e := p.unary()
		return func(get func(string) (float64, error)) (float64, error) {
			v, err := e(get)
			return -v, err
		}
	}
	return p.atom()
}

func (p *exprParser) atom() expr {
	fail := func() expr {
		if p.err == nil {
			p.err = errExprSyntax
		}
		return func(func(string) (float64, error)) (float64, error) { return 0, errExprSyntax }
	}
	c := p.peek()
	start := p.i
	switch {
	case c == '(':
		p.i++
		e := p.sum()
		if p.peek() != ')' {
			return fail()
		}
		p.i++
		return e
	case c >= '0' && c <= '9':
		for p.i < len(p.s) && (p.s[p.i] >= '0' && p.s[p.i] <= '9' || p.s[p.i] == '.') {
			p.i++
		}
		v, err := strconv.ParseFloat(p.s[start:p.i], 64)
		if err != nil {
			return fail()
		}
		return func(func(string) (float64, error)) (float64, error) { return v, nil }
	case c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z':
		for p.i < len(p.s) && (p.s[p.i] >= 'A' && p.s[p.i] <= 'Z' || p.s[p.i] >= 'a' && p.s[p.i] <= 'z' || p.s[p.i] >= '0' && p.s[p.i] <= '9') {
			p.i++
		}
		name := p.s[start:p.i]
		p.names = append(p.names, name)
		return func(get func(string) (float64, error)) (float64, error) { return get(name) }
	}
	return fail()
}

// evalExpr computes an expression against fields, rounded to places.
func evalExpr(s string, fields map[string]string, places int) (string, bool) {
	e, _, err := parseExpr(s)
	if err != nil {
		return "", false
	}
	v, err := e(func(name string) (float64, error) {
		n, ok := Number(fields[name])
		if !ok {
			return 0, errNotNumber
		}
		return n, nil
	})
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return "", false
	}
	return FormatNumber(roundTo(v, places)), true
}

// roundTo rounds half away from zero, spelled out so the agent and the
// dashboard round the same last digit.
func roundTo(v float64, places int) float64 {
	p := math.Pow10(places)
	r := math.Floor(math.Abs(v)*p+0.5) / p
	if v < 0 {
		return -r
	}
	return r
}

// FormatNumber writes v as its shortest decimal: 400, 66.67, never 4e+02.
func FormatNumber(v float64) string {
	if v == 0 {
		return "0"
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// fill is what a template puts for the token inner, and whether the token is
// one: a literal in braces is not and stays as written.
func fill(inner string, fields map[string]string, places int) (value string, computed, ok, isToken bool) {
	if fieldName.MatchString(inner) {
		return fields[inner], false, true, true
	}
	if !isExpression(inner) {
		return "", false, false, false
	}
	v, ok := evalExpr(inner, fields, places)
	return v, true, ok, true
}

// RenderUSSD fills a USSD code as the phone does: no spaces in a field, an
// amount with zero decimals as a whole number ("200.0" is 200), an expression
// rounded to a whole number. What cannot be computed leaves "?", which
// IsDialable refuses: never a code that dials another amount.
func RenderUSSD(template string, fields map[string]string) string {
	return token.ReplaceAllStringFunc(template, func(m string) string {
		inner := m[1 : len(m)-1]
		v, computed, ok, isToken := fill(inner, fields, 0)
		switch {
		case !isToken:
			return m
		case !ok:
			return "?"
		case computed:
			return v
		}
		return USSDValue(v)
	})
}

// USSDValue is a field as it goes into a USSD code: see RenderUSSD.
func USSDValue(v string) string {
	compact := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r', ' ', ' ':
			return -1
		}
		return r
	}, v)
	if m := wholeAmount.FindStringSubmatch(compact); m != nil {
		return m[1]
	}
	return compact
}

// IsDialable is whether a filled USSD code can be dialled as it is: digits,
// * and #, and {pin} for the phone's vault.
func IsDialable(code string) bool { return dialable.MatchString(code) }

var (
	wholeAmount = regexp.MustCompile(`^([0-9]+)[.,]0+$`)
	dialable    = regexp.MustCompile(`^[*#]([0-9*#]|\{pin\})*#$`)
)
