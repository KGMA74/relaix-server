package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/KGMA74/relaix-server/api"
)

func TestExtraToolsAndAdminOnes(t *testing.T) {
	const adminKey = "admin-key"
	f := &fakeAPI{key: goodKey}
	auth := api.AuthenticatorFunc(func(r *http.Request) (api.Principal, error) {
		if tok, _ := api.BearerToken(r); tok == adminKey {
			return api.Principal{AllAccounts: true}, nil
		}
		return f.authenticator().Authenticate(r)
	})
	echo := func(_ context.Context, _ Caller, args json.RawMessage) (any, error) { return string(args), nil }
	s, err := New(Options{API: f, Authenticator: auth, Secret: []byte("s"), Tools: []Tool{
		{Name: "everyone", Title: "E", Description: "d", Input: schema(obj{}), Call: echo},
		{Name: "admin_only", Title: "A", Description: "d", Input: schema(obj{}), AdminOnly: true, Call: echo},
		{Name: "fails", Title: "F", Description: "d", Input: schema(obj{}), Call: func(context.Context, Caller, json.RawMessage) (any, error) {
			return nil, errors.Join(Errorf("bad input"))
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.Routes(mux)

	names := func(key string) map[string]bool {
		_, out := rpc(t, mux, key, "tools/list", nil)
		got := map[string]bool{}
		for _, d := range out["result"].(map[string]any)["tools"].([]any) {
			got[d.(map[string]any)["name"].(string)] = true
		}
		return got
	}
	if got := names(goodKey); !got["everyone"] || got["admin_only"] || !got["send_sms"] {
		t.Errorf("an account sees %v", got)
	}
	if got := names(adminKey); !got["admin_only"] {
		t.Errorf("the administrator sees %v", got)
	}

	// To an account it does not exist.
	if _, out := rpc(t, mux, goodKey, "tools/call", map[string]any{"name": "admin_only", "arguments": map[string]any{}}); out["error"] == nil {
		t.Error("an account ran an admin tool")
	}
	if text, isErr := call(t, mux, adminKey, "admin_only", map[string]any{"x": 1}); isErr || text != `{"x":1}` {
		t.Errorf("admin_only = %q, %v", text, isErr)
	}
	if text, isErr := call(t, mux, goodKey, "fails", map[string]any{}); !isErr || text != "bad input" {
		t.Errorf("fails = %q, %v", text, isErr)
	}
}
