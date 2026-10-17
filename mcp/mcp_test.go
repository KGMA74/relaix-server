package mcp

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/KGMA74/relaix-server/api"
	"github.com/KGMA74/relaix-server/policy"
)

const goodKey = "rlx_test_key"

// fakeAPI stands for the gateway's REST API: one phone, its policy, and a
// record of what was asked.
type fakeAPI struct {
	mu       sync.Mutex
	key      string // the only key accepted
	policy   policy.Policy
	requests []string
	bodies   []string
}

func (f *fakeAPI) authenticator() api.Authenticator {
	return api.AuthenticatorFunc(func(r *http.Request) (api.Principal, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		tok, err := api.BearerToken(r)
		if err != nil || tok != f.key {
			return api.Principal{}, errors.New("invalid api key")
		}
		return api.Principal{}, nil
	})
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if _, err := f.authenticator().Authenticate(r); err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"code":"unauthorized","message":"invalid api key"}}`)
		return
	}
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
	f.bodies = append(f.bodies, string(body))
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == "GET" && r.URL.Path == "/devices":
		_ = json.NewEncoder(w).Encode(map[string]any{"devices": []any{map[string]any{
			"deviceId": "dev-1", "label": "Tecno", "capabilities": []string{"automations"}, "policy": f.policy,
		}}})
	case r.Method == "PUT" && r.URL.Path == "/devices/dev-1/policy":
		var p policy.Policy
		_ = json.Unmarshal(body, &p)
		p = policy.Normalize(p)
		if err := policy.Validate(p); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "invalid_policy", "message": err.Error()}})
			return
		}
		p.Version++
		f.policy = p
		_ = json.NewEncoder(w).Encode(p)
	case r.Method == "POST" && r.URL.Path == "/send":
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"jobs":[{"jobId":"j1","status":"pending"}]}`)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"no such route"}}`)
	}
}

func newServer(t *testing.T) (*Server, *fakeAPI, http.Handler) {
	t.Helper()
	f := &fakeAPI{key: goodKey}
	s, err := New(Options{API: f, Authenticator: f.authenticator(), Secret: []byte("test secret")})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.Routes(mux)
	return s, f, mux
}

// rpc posts one JSON-RPC call to /mcp with token, and decodes the answer.
func rpc(t *testing.T, h http.Handler, token, method string, params any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(string(raw)))
	req.Host = "relaix.example"
	req.Header.Set("X-Forwarded-Proto", "https")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// call runs a tool and answers its text and whether it failed.
func call(t *testing.T, h http.Handler, token, name string, args any) (string, bool) {
	t.Helper()
	code, out := rpc(t, h, token, "tools/call", map[string]any{"name": name, "arguments": args})
	if code != http.StatusOK {
		t.Fatalf("tools/call %s: status %d", name, code)
	}
	res := out["result"].(map[string]any)
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	return text, res["isError"].(bool)
}

func TestUnauthenticatedPointsToOAuth(t *testing.T) {
	_, _, h := newServer(t)
	for _, tok := range []string{"", "wrong", accessPrefix + "forged"} {
		req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{}`))
		req.Host = "relaix.example"
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("token %q: status %d, want 401", tok, rec.Code)
		}
		if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, `resource_metadata="http://relaix.example/.well-known/oauth-protected-resource"`) {
			t.Errorf("WWW-Authenticate = %q", got)
		}
	}
}

func TestBrowserOriginRefused(t *testing.T) {
	_, _, h := newServer(t)
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+goodKey)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", rec.Code)
	}
}

func TestInitializeAndList(t *testing.T) {
	_, _, h := newServer(t)
	_, out := rpc(t, h, goodKey, "initialize", map[string]any{"protocolVersion": "2025-03-26"})
	if v := out["result"].(map[string]any)["protocolVersion"]; v != "2025-03-26" {
		t.Errorf("protocolVersion = %v", v)
	}
	_, out = rpc(t, h, goodKey, "tools/list", nil)
	list := out["result"].(map[string]any)["tools"].([]any)
	if len(list) != len(tools) {
		t.Fatalf("%d tools listed, %d implemented", len(list), len(tools))
	}
	for _, d := range list {
		if name := d.(map[string]any)["name"].(string); tools[name] == nil {
			t.Errorf("listed tool %q has no implementation", name)
		}
	}
}

func TestToolsCallTheAPIWithTheKey(t *testing.T) {
	_, f, h := newServer(t)
	text, isErr := call(t, h, goodKey, "send_sms", map[string]any{"recipients": []string{"+22670000000"}, "body": "Bonjour"})
	if isErr || !strings.Contains(text, "j1") {
		t.Fatalf("send_sms: %s", text)
	}
	if got := f.requests[0]; got != "POST /send" {
		t.Errorf("request = %q", got)
	}
	if !strings.Contains(f.bodies[0], `"recipients":["+22670000000"]`) {
		t.Errorf("body = %s", f.bodies[0])
	}

	// A misspelt argument is reported, not dropped.
	if text, isErr := call(t, h, goodKey, "send_sms", map[string]any{"recipient": "+22670000000", "body": "x"}); !isErr {
		t.Errorf("unknown argument accepted: %s", text)
	}
	// The API's refusal reaches the model with its message.
	if text, isErr := call(t, h, goodKey, "get_job", map[string]any{"jobId": "00000000-0000-0000-0000-000000000000"}); !isErr || !strings.Contains(text, "no such route") {
		t.Errorf("get_job: %s", text)
	}
}

func omAutomation() map[string]any {
	return map[string]any{
		"name":    "OM vers Moov",
		"enabled": true,
		"senders": []string{"OrangeMoney"},
		"pattern": `recu (?P<amount>[0-9 ]+) FCFA de (?P<from>[0-9]+)`,
		"actions": []any{
			map[string]any{"type": "webhook", "url": "https://example.com/hook", "body": `{"montant": {amount}, "de": "{from}"}`},
			map[string]any{"type": "ussd", "code": "*555*2*{from}*{amount}*{pin}#"},
		},
	}
}

func TestTestAutomation(t *testing.T) {
	_, f, h := newServer(t)
	text, isErr := call(t, h, goodKey, "test_automation", map[string]any{
		"automation": omAutomation(), "sender": "OrangeMoney", "body": "Vous avez recu 5000 FCFA de 70112233",
	})
	if isErr {
		t.Fatal(text)
	}
	for _, want := range []string{`"matches": true`, `{\"montant\": 5000, \"de\": \"70112233\"}`, `*555*2*70112233*5000*{pin}#`} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %s in %s", want, text)
		}
	}
	if len(f.requests) != 0 {
		t.Errorf("testing called the API: %v", f.requests)
	}

	text, _ = call(t, h, goodKey, "test_automation", map[string]any{
		"automation": omAutomation(), "sender": "+22670000000", "body": "Vous avez recu 5000 FCFA de 70112233",
	})
	if !strings.Contains(text, `"matches": false`) {
		t.Errorf("another sender matched: %s", text)
	}
}

func TestSaveAutomationGeneratesTheWebhookSecret(t *testing.T) {
	_, f, h := newServer(t)
	text, isErr := call(t, h, goodKey, "save_automation", map[string]any{"deviceId": "dev-1", "automation": omAutomation()})
	if isErr {
		t.Fatal(text)
	}
	if len(f.policy.Automations) != 1 {
		t.Fatalf("stored %d automations", len(f.policy.Automations))
	}
	a := f.policy.Automations[0]
	if !strings.HasPrefix(a.Actions[0].Secret, "whsec_") {
		t.Errorf("webhook secret = %q", a.Actions[0].Secret)
	}

	// Saved again under its id: replaced, not added.
	again := omAutomation()
	again["id"] = a.ID
	again["name"] = "Renamed"
	if text, isErr := call(t, h, goodKey, "save_automation", map[string]any{"deviceId": "dev-1", "automation": again}); isErr {
		t.Fatal(text)
	}
	if n := len(f.policy.Automations); n != 1 || f.policy.Automations[0].Name != "Renamed" {
		t.Fatalf("after replace: %+v", f.policy.Automations)
	}

	if text, isErr := call(t, h, goodKey, "delete_automation", map[string]any{"deviceId": "dev-1", "automationId": a.ID}); isErr {
		t.Fatal(text)
	}
	if len(f.policy.Automations) != 0 {
		t.Fatal("not deleted")
	}

	// Invalid: refused before anything is stored.
	bad := omAutomation()
	bad["pattern"] = "(?P<amount>"
	if _, isErr := call(t, h, goodKey, "save_automation", map[string]any{"deviceId": "dev-1", "automation": bad}); !isErr {
		t.Error("invalid pattern accepted")
	}
}

func TestDeviceRulesKeepTheAutomations(t *testing.T) {
	_, f, h := newServer(t)
	if text, isErr := call(t, h, goodKey, "save_automation", map[string]any{"deviceId": "dev-1", "automation": omAutomation()}); isErr {
		t.Fatal(text)
	}
	text, isErr := call(t, h, goodKey, "set_device_rules", map[string]any{
		"deviceId": "dev-1",
		"limits":   map[string]any{"perHour": 30, "perDay": 300},
		"accept":   map[string]any{"countries": []string{"bf"}},
		"availability": map[string]any{"timeZone": "Africa/Ouagadougou",
			"windows": []any{map[string]any{"days": []int{1, 2, 3, 4, 5}, "start": 480, "end": 1200}}},
	})
	if isErr {
		t.Fatal(text)
	}
	if f.policy.Limits.PerHour != 30 || f.policy.Accept.Countries[0] != "BF" || len(f.policy.Availability.Windows) != 1 {
		t.Fatalf("stored %+v", f.policy)
	}
	if len(f.policy.Automations) != 1 {
		t.Fatal("the automations were dropped")
	}

	// A section not given stays as it was.
	if text, isErr := call(t, h, goodKey, "set_device_rules", map[string]any{"deviceId": "dev-1", "inbound": map[string]any{"enabled": true}}); isErr {
		t.Fatal(text)
	}
	if f.policy.Limits.PerHour != 30 || !f.policy.Inbound.Enabled {
		t.Fatalf("after a partial change: %+v", f.policy)
	}

	got, isErr := call(t, h, goodKey, "get_device_rules", map[string]any{"deviceId": "dev-1"})
	var r struct {
		Limits      policy.Limits `json:"limits"`
		Automations int           `json:"automations"`
	}
	if isErr || json.Unmarshal([]byte(got), &r) != nil || r.Limits.PerHour != 30 || r.Automations != 1 {
		t.Fatalf("get_device_rules = %s", got)
	}

	if _, isErr := call(t, h, goodKey, "set_device_rules", map[string]any{"deviceId": "dev-1"}); !isErr {
		t.Error("an empty change accepted")
	}
	if _, isErr := call(t, h, goodKey, "set_device_rules", map[string]any{"deviceId": "dev-1", "limits": map[string]any{"perHour": -1}}); !isErr {
		t.Error("a negative cap accepted")
	}
}

// ---------------------------------------------------------------------------
// OAuth
// ---------------------------------------------------------------------------

const callback = "https://claude.ai/api/mcp/auth_callback"

func do(h http.Handler, method, target string, form url.Values) *httptest.ResponseRecorder {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, target, body)
	req.Host = "relaix.example"
	req.Header.Set("X-Forwarded-Proto", "https")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func register(t *testing.T, h http.Handler, redirect string) (string, int) {
	t.Helper()
	req := httptest.NewRequest("POST", "/oauth/register", strings.NewReader(`{"client_name":"Claude","redirect_uris":["`+redirect+`"]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out struct {
		ClientID string `json:"client_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out.ClientID, rec.Code
}

func TestOAuthFlow(t *testing.T) {
	_, f, h := newServer(t)

	if _, code := register(t, h, "https://evil.example/cb"); code != http.StatusBadRequest {
		t.Fatalf("foreign redirect registered: %d", code)
	}
	client, code := register(t, h, callback)
	if code != http.StatusCreated || !strings.HasPrefix(client, clientPrefix) {
		t.Fatalf("register: %d %q", code, client)
	}

	verifier := strings.Repeat("v", 50)
	sum := sha256.Sum256([]byte(verifier))
	params := url.Values{
		"client_id": {client}, "redirect_uri": {callback}, "response_type": {"code"}, "state": {"st"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		"resource": {"https://relaix.example/mcp"},
	}
	if rec := do(h, "GET", "/oauth/authorize?"+params.Encode(), nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Claude") {
		t.Fatalf("authorize page: %d", rec.Code)
	}

	// A wrong key: the form again, nothing sent to the client.
	form := url.Values{}
	for k, v := range params {
		form[k] = v
	}
	form.Set("decision", "allow")
	form.Set("key", "wrong")
	if rec := do(h, "POST", "/oauth/authorize", form); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong key: %d", rec.Code)
	}

	form.Set("key", goodKey)
	rec := do(h, "POST", "/oauth/authorize", form)
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusFound || !strings.HasPrefix(loc.String(), callback) || loc.Query().Get("state") != "st" {
		t.Fatalf("authorize: %d %s", rec.Code, loc)
	}
	authCode := loc.Query().Get("code")

	exchange := url.Values{"grant_type": {"authorization_code"}, "code": {authCode}, "client_id": {client},
		"redirect_uri": {callback}, "code_verifier": {"w" + verifier[1:]}}
	if rec := do(h, "POST", "/oauth/token", exchange); rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong verifier accepted: %d", rec.Code)
	}
	exchange.Set("code_verifier", verifier)
	rec = do(h, "POST", "/oauth/token", exchange)
	var tokens struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &tokens)
	if rec.Code != http.StatusOK || !strings.HasPrefix(tokens.Access, accessPrefix) {
		t.Fatalf("token: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(tokens.Access, goodKey) {
		t.Fatal("the key shows in the token")
	}
	if rec := do(h, "POST", "/oauth/token", exchange); rec.Code != http.StatusBadRequest {
		t.Fatalf("code used twice: %d", rec.Code)
	}

	// The access token works on /mcp, and acts with the key.
	if text, isErr := call(t, h, tokens.Access, "list_devices", map[string]any{}); isErr || !strings.Contains(text, "dev-1") {
		t.Fatalf("list_devices by oauth: %s", text)
	}
	// A refresh token is no access token.
	if code, _ := rpc(t, h, tokens.Refresh, "ping", nil); code != http.StatusUnauthorized {
		t.Fatalf("refresh token used as access: %d", code)
	}

	refresh := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tokens.Refresh}, "client_id": {client}}
	if rec := do(h, "POST", "/oauth/token", refresh); rec.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", rec.Code, rec.Body)
	}

	// The key is revoked: the access token stops working, the refresh too.
	f.mu.Lock()
	f.key = "another"
	f.mu.Unlock()
	if code, _ := rpc(t, h, tokens.Access, "ping", nil); code != http.StatusUnauthorized {
		t.Fatalf("revoked key still works: %d", code)
	}
	if rec := do(h, "POST", "/oauth/token", refresh); rec.Code != http.StatusBadRequest {
		t.Fatalf("refresh after revocation: %d", rec.Code)
	}
}

func TestOAuthDeny(t *testing.T) {
	_, _, h := newServer(t)
	client, _ := register(t, h, callback)
	sum := sha256.Sum256([]byte(strings.Repeat("v", 50)))
	form := url.Values{"client_id": {client}, "redirect_uri": {callback}, "response_type": {"code"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		"decision": {"deny"}, "key": {goodKey}}
	rec := do(h, "POST", "/oauth/authorize", form)
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if loc.Query().Get("error") != "access_denied" || loc.Query().Get("code") != "" {
		t.Fatalf("deny: %s", loc)
	}

	// A client id from another gateway's secret does not open here.
	other, _ := New(Options{API: &fakeAPI{}, Authenticator: (&fakeAPI{}).authenticator(), Secret: []byte("other")})
	foreign, _ := other.seal.seal(kindClient, clientPrefix, clientClaims{Name: "x", Redirects: []string{callback}})
	form.Set("client_id", foreign)
	if rec := do(h, "POST", "/oauth/authorize", form); rec.Code != http.StatusBadRequest {
		t.Fatalf("foreign client: %d", rec.Code)
	}
}
