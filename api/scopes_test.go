package api

import (
	"net/http/httptest"
	"testing"
)

func TestScopes(t *testing.T) {
	sender := Principal{Scopes: []string{ScopeSMSSend}}
	reader := Principal{Scopes: []string{ScopeMessagesRead, ScopeDevicesRead}}
	all := Principal{}
	cases := []struct {
		p      Principal
		method string
		path   string
		ok     bool
	}{
		{sender, "POST", "/send", true},
		{sender, "POST", "/ussd", false},
		{sender, "DELETE", "/jobs/abc", true},
		{sender, "GET", "/jobs", false},
		{reader, "GET", "/jobs/abc/events", true},
		{reader, "GET", "/devices", true},
		{reader, "PUT", "/devices/abc/policy", false},
		{reader, "POST", "/admin/devices/enroll-token", false},
		{reader, "GET", "/inbox", false},
		{reader, "GET", "/ui-config", true},
		{reader, "GET", "/cloud/account", true},
		{all, "PUT", "/devices/abc/policy", true},
		{Principal{Scopes: []string{}}, "GET", "/jobs", false},
	}
	for _, c := range cases {
		_, ok := permitted(c.p, httptest.NewRequest(c.method, c.path, nil))
		if ok != c.ok {
			t.Errorf("%v %s %s: permitted = %v", c.p.Scopes, c.method, c.path, ok)
		}
	}
}
