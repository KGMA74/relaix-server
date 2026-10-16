package mcp

/*
OAuth 2.1 for remote connectors (claude.ai).

	A claude.ai custom connector cannot send a fixed Authorization header: it
	knows "no authentication" or OAuth. This is the flow the MCP specification
	expects of a server:

	  1. /mcp without credentials answers 401, naming its metadata (RFC 9728);
	  2. the client reads the authorization server's metadata (RFC 8414);
	  3. it registers (RFC 7591), as a public client without a secret;
	  4. it sends the browser to /oauth/authorize (code + PKCE S256);
	  5. it trades the code for an access token and a refresh token.

	Consent is an API key: the authorize page asks for one, the gateway's
	authenticator checks it, and every token issued afterwards stands for that
	key — the account it sees, nothing more. In the cloud, that is a key the
	account's owner made for the assistant (Account → API keys); revoking it
	disconnects the assistant. Signing in with the dashboard's session would
	not work anyway: its cookie is SameSite=Strict, and this page is reached
	from claude.ai.

	Nothing is stored (seal.go). Client ids, codes and tokens are sealed;
	only the codes already exchanged are remembered, in memory, until they
	expire, so that a code serves once.

	Codes only go to the redirect addresses on the list (claude.ai's by
	default): even granted by mistake, a code cannot leave for anywhere else.
*/

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	clientPrefix  = "rlxc_"
	codePrefix    = "rlxk_"
	accessPrefix  = "rlxat_"
	refreshPrefix = "rlxrt_"

	kindClient  = "client"
	kindCode    = "code"
	kindAccess  = "access"
	kindRefresh = "refresh"

	accessTTL  = time.Hour
	refreshTTL = 30 * 24 * time.Hour
	codeTTL    = 2 * time.Minute
)

// DefaultRedirects are claude.ai's connector callbacks.
var DefaultRedirects = []string{
	"https://claude.ai/api/mcp/auth_callback",
	"https://claude.com/api/mcp/auth_callback",
}

type clientClaims struct {
	Name      string   `json:"n"`
	Redirects []string `json:"r"`
}

type codeClaims struct {
	Key       string `json:"k"`
	Client    string `json:"c"` // digest of the client id
	Redirect  string `json:"r"`
	Challenge string `json:"p"`
	Expires   int64  `json:"x"`
}

// tokenClaims are an access or a refresh token's.
type tokenClaims struct {
	Key     string `json:"k"`
	Client  string `json:"c"`
	Expires int64  `json:"x"`
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// metadata
// ---------------------------------------------------------------------------

func (s *Server) resourceMetadata(w http.ResponseWriter, r *http.Request) {
	base := baseURL(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 base + "/mcp",
		"authorization_servers":    []string{base},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            s.opts.Name,
	})
}

func (s *Server) serverMetadata(w http.ResponseWriter, r *http.Request) {
	base := baseURL(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                base,
		"authorization_endpoint":                base + "/oauth/authorize",
		"token_endpoint":                        base + "/oauth/token",
		"registration_endpoint":                 base + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
	})
}

func oauthError(w http.ResponseWriter, status int, code, description string) {
	body := map[string]string{"error": code}
	if description != "" {
		body["error_description"] = description
	}
	writeJSON(w, status, body)
}

// ---------------------------------------------------------------------------
// registration (RFC 7591)
// ---------------------------------------------------------------------------

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RedirectURIs []string `json:"redirect_uris"`
		ClientName   string   `json:"client_name"`
	}
	// Unknown fields are fine: the RFC lists many optional ones, all ignored.
	if err := jsonDecode(w, r, &body); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "unreadable JSON")
		return
	}
	if len(body.RedirectURIs) == 0 || len(body.RedirectURIs) > 5 {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "1 to 5 redirect URIs")
		return
	}
	for _, u := range body.RedirectURIs {
		if !slices.Contains(s.opts.Redirects, u) {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect URI not allowed: "+u)
			return
		}
	}
	name := strings.TrimSpace(strings.ReplaceAll(body.ClientName, "\n", " "))
	if name == "" {
		name = "MCP client"
	}
	if utf8.RuneCountInString(name) > 80 {
		name = string([]rune(name)[:80])
	}
	id, err := s.seal.seal(kindClient, clientPrefix, clientClaims{Name: name, Redirects: body.RedirectURIs})
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  id,
		"client_id_issued_at":        s.opts.Now().Unix(),
		"client_name":                name,
		"redirect_uris":              body.RedirectURIs,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
}

// ---------------------------------------------------------------------------
// authorization
// ---------------------------------------------------------------------------

// authz is a validated authorization request.
type authz struct {
	ClientID    string
	Client      clientClaims
	RedirectURI string
	State       string
	Challenge   string
	Resource    string
}

// parseAuthz validates an authorization request. Until the client and its
// redirect URI are known good, a problem is shown on the page (bad != "");
// after that it goes back to the client as an OAuth error (code != "").
func (s *Server) parseAuthz(r *http.Request, q url.Values) (a authz, bad, code string) {
	a = authz{ClientID: q.Get("client_id"), RedirectURI: q.Get("redirect_uri"), State: q.Get("state"),
		Challenge: q.Get("code_challenge"), Resource: q.Get("resource")}
	if s.seal.open(kindClient, clientPrefix, a.ClientID, &a.Client) != nil ||
		!slices.Contains(a.Client.Redirects, a.RedirectURI) || !slices.Contains(s.opts.Redirects, a.RedirectURI) {
		return a, "Client inconnu ou adresse de retour non autorisée. Relancez la connexion depuis votre assistant.", ""
	}
	if q.Get("response_type") != "code" {
		return a, "", "unsupported_response_type"
	}
	if q.Get("code_challenge_method") != "S256" || !validChallenge(a.Challenge) {
		return a, "", "invalid_request"
	}
	if a.Resource != "" && strings.TrimSuffix(a.Resource, "/") != baseURL(r)+"/mcp" {
		return a, "", "invalid_target"
	}
	return a, "", ""
}

// validChallenge: an S256 digest in base64url, 43 characters.
func validChallenge(c string) bool {
	b, err := base64.RawURLEncoding.DecodeString(c)
	return err == nil && len(b) == sha256.Size
}

func redirectWith(w http.ResponseWriter, r *http.Request, redirectURI string, params map[string]string) {
	u, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, "invalid redirect URI", http.StatusBadRequest)
		return
	}
	q := u.Query()
	for k, v := range params {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *Server) authorizePage(w http.ResponseWriter, r *http.Request) {
	a, bad, code := s.parseAuthz(r, r.URL.Query())
	switch {
	case bad != "":
		s.render(w, http.StatusBadRequest, page{Name: s.opts.Name, Title: "Demande invalide", Message: bad})
	case code != "":
		redirectWith(w, r, a.RedirectURI, map[string]string{"error": code, "state": a.State, "iss": baseURL(r)})
	default:
		s.render(w, http.StatusOK, s.formPage(a, ""))
	}
}

func (s *Server) formPage(a authz, problem string) page {
	host := a.RedirectURI
	if u, err := url.Parse(a.RedirectURI); err == nil {
		host = u.Host
	}
	return page{Name: s.opts.Name, Title: "Connecter un assistant", Form: true, Problem: problem,
		Client: a.Client.Name, Host: host,
		Fields: map[string]string{
			"client_id": a.ClientID, "redirect_uri": a.RedirectURI, "state": a.State,
			"code_challenge": a.Challenge, "code_challenge_method": "S256",
			"response_type": "code", "resource": a.Resource,
		}}
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		s.render(w, http.StatusBadRequest, page{Name: s.opts.Name, Title: "Demande invalide", Message: "Formulaire illisible."})
		return
	}
	a, bad, code := s.parseAuthz(r, r.PostForm)
	switch {
	case bad != "":
		s.render(w, http.StatusBadRequest, page{Name: s.opts.Name, Title: "Demande invalide", Message: bad})
		return
	case code != "":
		redirectWith(w, r, a.RedirectURI, map[string]string{"error": code, "state": a.State, "iss": baseURL(r)})
		return
	}
	if r.PostForm.Get("decision") != "allow" {
		redirectWith(w, r, a.RedirectURI, map[string]string{"error": "access_denied", "state": a.State, "iss": baseURL(r)})
		return
	}
	if !s.auth.allow(clientIP(r), s.opts.Now()) {
		s.render(w, http.StatusTooManyRequests, s.formPage(a, "Trop d'essais. Réessayez dans quelques minutes."))
		return
	}
	key := strings.TrimSpace(r.PostForm.Get("key"))
	if _, err := s.opts.Authenticator.Authenticate(keyRequest(r.Context(), key)); err != nil {
		s.render(w, http.StatusUnauthorized, s.formPage(a, "Clé refusée : "+err.Error()))
		return
	}
	c, err := s.seal.seal(kindCode, codePrefix, codeClaims{Key: key, Client: digest(a.ClientID),
		Redirect: a.RedirectURI, Challenge: a.Challenge, Expires: s.opts.Now().Add(codeTTL).Unix()})
	if err != nil {
		s.render(w, http.StatusInternalServerError, page{Name: s.opts.Name, Title: "Erreur", Message: "Réessayez."})
		return
	}
	s.opts.Logger.Info("mcp client authorized", "client", a.Client.Name)
	redirectWith(w, r, a.RedirectURI, map[string]string{"code": c, "state": a.State, "iss": baseURL(r)})
}

// ---------------------------------------------------------------------------
// tokens
// ---------------------------------------------------------------------------

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "unreadable form")
		return
	}
	f := r.PostForm
	switch f.Get("grant_type") {
	case "authorization_code":
		s.exchangeCode(w, r, f)
	case "refresh_token":
		s.refresh(w, r, f)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "")
	}
}

// pkceOK checks code_verifier (RFC 7636) against the challenge.
func pkceOK(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(challenge)) == 1
}

// spend marks a code used, reporting whether it was still unused.
func (s *Server) spend(code string, expires time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opts.Now()
	for h, exp := range s.used {
		if now.After(exp) {
			delete(s.used, h)
		}
	}
	h := digest(code)
	if _, done := s.used[h]; done {
		return false
	}
	s.used[h] = expires
	return true
}

func (s *Server) exchangeCode(w http.ResponseWriter, r *http.Request, f url.Values) {
	code := f.Get("code")
	var c codeClaims
	if s.seal.open(kindCode, codePrefix, code, &c) != nil {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "")
		return
	}
	expires := time.Unix(c.Expires, 0)
	if s.opts.Now().After(expires) || c.Client != digest(f.Get("client_id")) ||
		c.Redirect != f.Get("redirect_uri") || !pkceOK(f.Get("code_verifier"), c.Challenge) ||
		!s.spend(code, expires) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "")
		return
	}
	s.issue(w, r, c.Key, c.Client)
}

// refresh trades a refresh token for a new pair. The key inside is checked
// again: one revoked since ends the connection here.
func (s *Server) refresh(w http.ResponseWriter, r *http.Request, f url.Values) {
	var c tokenClaims
	if s.seal.open(kindRefresh, refreshPrefix, f.Get("refresh_token"), &c) != nil ||
		s.opts.Now().Unix() > c.Expires ||
		(f.Get("client_id") != "" && c.Client != digest(f.Get("client_id"))) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "")
		return
	}
	s.issue(w, r, c.Key, c.Client)
}

// issue answers a fresh token pair for key, once the authenticator still
// accepts it.
func (s *Server) issue(w http.ResponseWriter, r *http.Request, key, client string) {
	if _, err := s.opts.Authenticator.Authenticate(keyRequest(r.Context(), key)); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the API key was revoked or its account suspended")
		return
	}
	now := s.opts.Now()
	access, err1 := s.seal.seal(kindAccess, accessPrefix, tokenClaims{Key: key, Client: client, Expires: now.Add(accessTTL).Unix()})
	refresh, err2 := s.seal.seal(kindRefresh, refreshPrefix, tokenClaims{Key: key, Client: client, Expires: now.Add(refreshTTL).Unix()})
	if err1 != nil || err2 != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    int(accessTTL.Seconds()),
		"refresh_token": refresh,
	})
}

// ---------------------------------------------------------------------------
// page
// ---------------------------------------------------------------------------

type page struct {
	Name    string
	Title   string
	Message string
	Form    bool
	Problem string
	Client  string
	Host    string
	Fields  map[string]string
}

// No script: the page works with the strictest policy there is.
var pageTmpl = template.Must(template.New("oauth").Parse(`<!doctype html>
<html lang="fr"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>{{.Title}} · {{.Name}}</title>
<style>
:root{color-scheme:light dark;--bg:#f7f7f8;--card:#fff;--fg:#18181b;--muted:#5f5f6b;--line:#e4e4e7;--accent:#2563eb;--bad:#b91c1c}
@media (prefers-color-scheme:dark){:root{--bg:#0f0f12;--card:#18181c;--fg:#ececf1;--muted:#a1a1aa;--line:#2e2e35;--accent:#60a5fa;--bad:#f87171}}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;display:grid;place-items:center;background:var(--bg);color:var(--fg);font:15px/1.5 system-ui,sans-serif;padding:16px}
main{width:100%;max-width:440px;background:var(--card);border:1px solid var(--line);border-radius:12px;padding:28px 24px}
.k{font:12px ui-monospace,monospace;letter-spacing:.1em;text-transform:uppercase;color:var(--muted);margin:0}
h1{font-size:22px;margin:6px 0 12px}
p{color:var(--muted);margin:0 0 12px}
strong{color:var(--fg)}
label{display:block;font-weight:600;margin:16px 0 6px}
input[type=password]{width:100%;padding:10px 12px;border:1px solid var(--line);border-radius:8px;background:transparent;color:var(--fg);font:14px ui-monospace,monospace}
.row{display:flex;gap:8px;margin-top:16px}
button{flex:1;padding:10px 12px;border-radius:8px;border:1px solid var(--line);background:transparent;color:var(--fg);font:600 14px system-ui,sans-serif;cursor:pointer}
button.go{background:var(--accent);border-color:var(--accent);color:#fff}
.bad{color:var(--bad)}
small{display:block;color:var(--muted);margin-top:6px}
</style></head>
<body><main>
<p class="k">{{.Name}} · MCP</p>
<h1>{{.Title}}</h1>
{{if .Form}}
<p><strong>{{.Client}}</strong> demande à utiliser votre passerelle : envoyer des SMS, lancer des codes USSD, lire les SMS reçus et écrire les automatisations de vos téléphones. Le code d'autorisation part vers <strong>{{.Host}}</strong>.</p>
{{if .Problem}}<p class="bad">{{.Problem}}</p>{{end}}
<form method="post" action="/oauth/authorize">
{{range $k, $v := .Fields}}<input type="hidden" name="{{$k}}" value="{{$v}}">{{end}}
<label for="key">Clé API</label>
<input id="key" name="key" type="password" autocomplete="off" autofocus>
<small>L'assistant aura exactement les droits de cette clé. Créez-en une pour lui : la révoquer le déconnecte.</small>
<div class="row">
<button type="submit" name="decision" value="deny">Refuser</button>
<button type="submit" name="decision" value="allow" class="go">Autoriser</button>
</div>
</form>
{{else}}
<p>{{.Message}}</p>
{{end}}
</main></body></html>`))

func (s *Server) render(w http.ResponseWriter, status int, p page) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// Never framed: a page that takes a key must not sit under someone
	// else's clicks.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	if err := pageTmpl.Execute(w, p); err != nil {
		s.opts.Logger.Error("could not render the oauth page", "err", err)
	}
}

func jsonDecode(w http.ResponseWriter, r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(v)
}
