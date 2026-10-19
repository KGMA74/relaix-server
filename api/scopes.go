package api

import (
	"net/http"
	"slices"
	"strings"
)

// Permissions a key may be limited to, in the manner of fine-grained tokens:
// one per kind of thing it can see or do. A principal without a list holds
// them all; one with a list holds only those.
const (
	// ScopeMessagesRead reads sent SMS and USSD jobs, their history and the
	// statistics, and follows live events.
	ScopeMessagesRead = "messages:read"
	// ScopeSMSSend sends SMS, and cancels them before they leave.
	ScopeSMSSend = "sms:send"
	// ScopeUSSDRun runs USSD codes, and cancels them before they run.
	ScopeUSSDRun = "ussd:run"
	// ScopeInboxRead reads received SMS and what automations did.
	ScopeInboxRead = "inbox:read"
	// ScopeDevicesRead lists the phones, their rules and automations, and the
	// automation library.
	ScopeDevicesRead = "devices:read"
	// ScopeDevicesWrite changes the phones: rules, automations, labels,
	// enrolling one, removing one.
	ScopeDevicesWrite = "devices:write"
	// ScopePaymentsRead and ScopePaymentsWrite read and create payments, on
	// a gateway that has them (the hosted platform). The routes check them.
	ScopePaymentsRead  = "payments:read"
	ScopePaymentsWrite = "payments:write"
)

// AllScopes lists every permission, in the order a form shows them.
var AllScopes = []string{ScopeSMSSend, ScopeUSSDRun, ScopeMessagesRead, ScopeInboxRead, ScopeDevicesRead, ScopeDevicesWrite,
	ScopePaymentsRead, ScopePaymentsWrite}

// ValidScope reports whether s names a permission.
func ValidScope(s string) bool { return slices.Contains(AllScopes, s) }

// Can reports whether p holds the permission.
func (p Principal) Can(scope string) bool {
	return p.Scopes == nil || slices.Contains(p.Scopes, scope)
}

// requiredScopes is what a gateway route needs: any one of the list. A route
// not listed — the probes, the dashboard, an embedding program's own routes —
// needs none here; those check their own.
func requiredScopes(r *http.Request) []string {
	path, get := r.URL.Path, r.Method == http.MethodGet || r.Method == http.MethodHead
	under := func(prefix string) bool { return path == prefix || strings.HasPrefix(path, prefix+"/") }
	switch {
	case path == "/send" && r.Method == http.MethodPost:
		return []string{ScopeSMSSend}
	case path == "/ussd" && r.Method == http.MethodPost:
		return []string{ScopeUSSDRun}
	case under("/jobs") && r.Method == http.MethodDelete:
		return []string{ScopeSMSSend, ScopeUSSDRun}
	case get && (under("/jobs") || under("/stats") || path == "/events"):
		return []string{ScopeMessagesRead}
	case get && (under("/inbox") || path == "/automations/runs"):
		return []string{ScopeInboxRead}
	case get && (path == "/devices" || path == "/countries" || path == "/automations/library" || path == "/automations/locks"):
		return []string{ScopeDevicesRead}
	case under("/devices") || path == "/admin/devices/enroll-token":
		return []string{ScopeDevicesWrite}
	}
	return nil
}

// permitted reports whether p may take the request, and if not which
// permission it lacks.
func permitted(p Principal, r *http.Request) (string, bool) {
	need := requiredScopes(r)
	if len(need) == 0 || slices.ContainsFunc(need, p.Can) {
		return "", true
	}
	return strings.Join(need, " or "), false
}
