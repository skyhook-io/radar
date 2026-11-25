package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/skyhook-io/radar/pkg/urlutil"
)

const (
	mcpAccessTTL        = 10 * time.Minute
	mcpGrantTTL         = 24 * time.Hour
	mcpPendingTTL       = 10 * time.Minute
	mcpClientTTL        = 30 * 24 * time.Hour
	mcpStoreLimit       = 4096
	mcpPendingPerClient = 8
	mcpConsentCookie    = "radar_mcp_consent_"
)

type mcpClient struct {
	ID            string    `json:"client_id"`
	Name          string    `json:"client_name,omitempty"`
	RedirectURIs  []string  `json:"redirect_uris"`
	AuthMethod    string    `json:"token_endpoint_auth_method"`
	GrantTypes    []string  `json:"grant_types"`
	ResponseTypes []string  `json:"response_types"`
	Expires       time.Time `json:"-"`
}

type mcpAuthorization struct {
	ClientID, RedirectURI, Resource, State, Challenge string
	CSRF, SID                                         string
	Expires                                           time.Time
}

type mcpGrant struct {
	ClientID, Resource, SID string
	ReturnHost              string
	AccessHash, RefreshID   string
	User                    User
	Expires                 time.Time
	Revoked                 bool
}

type mcpCode struct {
	mcpAuthorization
	Grant *mcpGrant
	Used  bool
}

type mcpToken struct {
	Grant       *mcpGrant
	Expires     time.Time
	RefreshHash string
}

// MCPOAuthServer provides browser-authorized, audience-bound MCP credentials.
// State is deliberately process-local; restarts require clients to authorize again.
type MCPOAuthServer struct {
	cfg                      Config
	basePath, origin, issuer string
	resources                map[string]string
	mu                       sync.Mutex
	clients                  map[string]mcpClient
	pending                  map[string]*mcpAuthorization
	codes                    map[string]*mcpCode
	access, refresh          map[string]*mcpToken
	now                      func() time.Time
	registrationWindow       time.Time
	registrations            int
}

// NewMCPOAuthServer creates an OAuth server for the supplied mounted MCP paths.
func NewMCPOAuthServer(cfg Config, basePath string, resources []string) (*MCPOAuthServer, error) {
	if cfg.Mode != "oidc" || cfg.Secret == "" {
		return nil, fmt.Errorf("MCP OAuth requires OIDC authentication and a session secret")
	}
	u, err := url.Parse(cfg.OIDCRedirectURL)
	if err != nil || u.User != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.Path != basePath+"/auth/callback" || u.Scheme != "https" {
		return nil, fmt.Errorf("MCP OAuth requires an HTTPS OIDC redirect URL at %s/auth/callback", basePath)
	}
	origin := u.Scheme + "://" + u.Host
	s := &MCPOAuthServer{cfg: cfg, basePath: basePath, origin: origin, issuer: origin + basePath,
		resources: make(map[string]string), clients: make(map[string]mcpClient), pending: make(map[string]*mcpAuthorization),
		codes: make(map[string]*mcpCode), access: make(map[string]*mcpToken), refresh: make(map[string]*mcpToken), now: time.Now}
	for _, path := range resources {
		if path != "/mcp" && path != "/mcp-readonly" {
			return nil, fmt.Errorf("unsupported MCP OAuth resource %q", path)
		}
		s.resources[path] = s.issuer + path
	}
	return s, nil
}

func mcpSecureURL(u *url.URL) bool {
	if u.Scheme == "https" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return u.Scheme == "http" && (u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback()))
}

// The consent page shows the host as written, so an international or invisible
// character could imitate another host; only the encoded (xn--) form is accepted.
func mcpASCIIHost(host string) bool {
	for i := 0; i < len(host); i++ {
		if host[i] <= ' ' || host[i] >= 0x7f {
			return false
		}
	}
	return true
}

// Native loopback callbacks may choose a new ephemeral port (RFC 8252 section 7.3).
func mcpRedirectAllowed(registered []string, raw string) bool {
	if slices.Contains(registered, raw) {
		return true
	}
	requested, err := url.Parse(raw)
	if err != nil || requested.Scheme != "http" || !mcpSecureURL(requested) {
		return false
	}
	for _, value := range registered {
		allowed, err := url.Parse(value)
		if err != nil || allowed.Scheme != requested.Scheme || allowed.Hostname() != requested.Hostname() {
			continue
		}
		allowed.Host = requested.Host
		if allowed.String() == requested.String() {
			return true
		}
	}
	return false
}

func (s *MCPOAuthServer) HandleMetadata(w http.ResponseWriter, r *http.Request) {
	mcpJSON(w, http.StatusOK, map[string]any{
		"issuer": s.issuer, "authorization_endpoint": s.issuer + "/auth/mcp/authorize",
		"token_endpoint": s.issuer + "/auth/mcp/token", "registration_endpoint": s.issuer + "/auth/mcp/register",
		"revocation_endpoint": s.issuer + "/auth/mcp/revoke", "response_types_supported": []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"token_endpoint_auth_methods_supported": []string{"none"}, "revocation_endpoint_auth_methods_supported": []string{"none"},
		"code_challenge_methods_supported": []string{"S256"}, "scopes_supported": []string{"mcp"},
	})
}

// ProtectedResourceMetadata serves metadata for exactly one MCP audience.
func (s *MCPOAuthServer) ProtectedResourceMetadata(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resource, ok := s.resources[path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		mcpJSON(w, http.StatusOK, map[string]any{"resource": resource, "authorization_servers": []string{s.issuer},
			"bearer_methods_supported": []string{"header"}, "scopes_supported": []string{"mcp"}})
	}
}

// HandleRegister registers public clients without trusting their display names.
func (s *MCPOAuthServer) HandleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		mcpError(w, 405, "invalid_request")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var client mcpClient
	d := json.NewDecoder(r.Body)
	err := d.Decode(&client)
	if err == nil {
		err = d.Decode(new(any))
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		mcpError(w, http.StatusRequestEntityTooLarge, "invalid_client_metadata")
		return
	}
	if err != io.EOF || len(client.RedirectURIs) == 0 || len(client.RedirectURIs) > 10 || len(client.Name) > 100 {
		mcpError(w, 400, "invalid_client_metadata")
		return
	}
	if client.AuthMethod != "" && client.AuthMethod != "none" {
		mcpError(w, 400, "invalid_client_metadata")
		return
	}
	for _, grant := range client.GrantTypes {
		if grant != "authorization_code" && grant != "refresh_token" {
			mcpError(w, 400, "invalid_client_metadata")
			return
		}
	}
	for _, response := range client.ResponseTypes {
		if response != "code" {
			mcpError(w, 400, "invalid_client_metadata")
			return
		}
	}
	for _, raw := range client.RedirectURIs {
		u, err := url.Parse(raw)
		if err != nil || len(raw) > 2048 || u.Host == "" || u.User != nil || u.Fragment != "" || !mcpSecureURL(u) || !mcpASCIIHost(u.Hostname()) {
			mcpError(w, 400, "invalid_redirect_uri")
			return
		}
	}
	client.Name = strings.Map(func(r rune) rune {
		if unicode.In(r, unicode.Cc, unicode.Cf) {
			return -1
		}
		return r
	}, client.Name)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune()
	if len(s.clients) >= 1024 {
		mcpError(w, 503, "temporarily_unavailable")
		return
	}
	// Count accepted registrations so malformed traffic cannot starve clients
	// sharing an ingress. Forwarded client addresses are not a trusted boundary.
	if s.now().Sub(s.registrationWindow) >= time.Minute {
		s.registrationWindow = s.now()
		s.registrations = 0
	}
	if s.registrations >= 20 {
		w.Header().Set("Retry-After", "60")
		mcpError(w, http.StatusTooManyRequests, "temporarily_unavailable")
		return
	}
	s.registrations++
	client.ID = mcpRandom()
	client.AuthMethod = "none"
	client.GrantTypes = []string{"authorization_code", "refresh_token"}
	client.ResponseTypes = []string{"code"}
	// Unapproved registrations expire quickly so anonymous clients cannot fill
	// the long-lived client store without a user's consent.
	client.Expires = s.now().Add(mcpPendingTTL)
	s.clients[client.ID] = client
	var hosts []string
	for _, raw := range client.RedirectURIs {
		if u, err := url.Parse(raw); err == nil && !slices.Contains(hosts, u.Host) {
			hosts = append(hosts, u.Host)
		}
	}
	mcpAuditLog("client registered", mcpAuditFields{ClientID: client.ID, ClientName: client.Name, ReturnHost: strings.Join(hosts, ",")})
	mcpJSON(w, http.StatusCreated, client)
}

// Colors mirror Radar's theme tokens; the page cannot load the app's stylesheet
// under its CSP, so the style block is allowed by hash instead.
const mcpConsentStyle = `:root{color-scheme:light dark;--base:light-dark(#F0F3F9,#0B0F1E);--surface:light-dark(#F8FBFE,#14192A);--elevated:light-dark(#E6ECF5,#1E2338);--text:light-dark(#1A1C20,#F5F6F8);--muted:light-dark(#555860,#A9ADB8);--border:light-dark(#D8E0EE,#222840);--brand:light-dark(#4A7CC9,#6A9FE0);--brand-hover:light-dark(#3B66A8,#8BB5EA);--warn-bg:light-dark(#FEF3C7,rgba(245,158,11,.12));--warn-border:light-dark(#F59E0B,rgba(245,158,11,.45));--warn-text:light-dark(#92400E,#FBBF24)}
*{box-sizing:border-box}body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;padding:24px 16px;background:var(--base);color:var(--text);font:14px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif}
main{width:100%;max-width:520px;background:var(--surface);border:1px solid var(--border);border-radius:14px;padding:24px;box-shadow:0 8px 24px rgba(15,23,42,.08)}
.brand{font-weight:600;color:var(--brand);letter-spacing:.02em}h1{font-size:18px;margin:4px 0 4px}.muted{color:var(--muted)}
.warn{margin:16px 0;padding:12px 14px;border:1px solid var(--warn-border);background:var(--warn-bg);border-radius:10px}.warn strong{color:var(--warn-text)}.warn p{margin:6px 0 0}
dl{margin:16px 0;display:grid;grid-template-columns:max-content 1fr;gap:10px 16px}dt{color:var(--muted)}dd{margin:0;min-width:0;overflow-wrap:anywhere}
code{font:12px/1.4 ui-monospace,SFMono-Regular,Menlo,monospace;background:var(--elevated);border-radius:4px;padding:1px 4px}
.actions{display:flex;justify-content:flex-end;gap:8px;margin-top:20px}button{font:inherit;font-weight:500;padding:8px 16px;border-radius:10px;cursor:pointer}
.deny{background:var(--surface);color:var(--text);border:1px solid var(--border)}.deny:hover{background:var(--elevated)}
.allow{background:var(--brand);color:#fff;border:1px solid transparent}.allow:hover{background:var(--brand-hover)}
@media (max-width:480px){dl{grid-template-columns:1fr;gap:2px}dd{margin-bottom:8px}}`

var mcpConsentStyleCSP = func() string {
	sum := sha256.Sum256([]byte(mcpConsentStyle))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}()

var mcpConsentTemplate = template.Must(template.New("consent").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Authorize Radar MCP</title><style>` + mcpConsentStyle + `</style></head><body><main>
<div class="brand">Radar</div><h1>Allow an AI tool to use Radar?</h1><p class="muted">Signed in as <strong>{{.Username}}</strong></p>
{{if .ThirdParty}}<div class="warn" role="alert"><strong>Access will be sent to another server: {{.RedirectHost}}</strong><p>If you allow, whoever runs {{.RedirectHost}} can use Radar's AI tools as you, with your Kubernetes permissions, for up to {{.Lifetime}}. Allow only if you started this sign-in yourself and trust that server.</p></div>{{end}}
<dl><dt>Application</dt><dd>{{with .ClientName}}<strong>{{.}}</strong> <span class="muted">(name provided by the application)</span>{{else}}<span class="muted">No name provided</span>{{end}}</dd>
<dt>Client ID</dt><dd><code>{{.ClientID}}</code></dd>
<dt>Endpoint</dt><dd><code>{{.Resource}}</code><br><span class="muted">{{.Capability}} Your Kubernetes permissions apply.</span></dd>
<dt>Access goes to</dt><dd><code>{{.RedirectURI}}</code><br><span class="muted">{{if .ThirdParty}}A server outside your computer.{{else}}An application on your computer.{{end}}</span></dd>
<dt>Lasts</dt><dd>Up to {{.Lifetime}}.</dd></dl>
<form method="post"><input type="hidden" name="request" value="{{.Request}}"><input type="hidden" name="csrf" value="{{.CSRF}}"><div class="actions"><button class="deny" name="decision" value="deny">Deny</button><button class="allow" name="decision" value="allow">Allow</button></div></form></main></body></html>`))

// mcpLocalRedirect reports whether the authorization code stays on the user's
// own machine; any other host receives a credential usable as the user.
func mcpLocalRedirect(raw string) (host string, local bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return raw, false
	}
	ip := net.ParseIP(u.Hostname())
	return u.Host, u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
}

// HandleAuthorize validates authorization requests and requires explicit browser consent.
func (s *MCPOAuthServer) HandleAuthorize(w http.ResponseWriter, r *http.Request) {
	mcpPrivateHeaders(w)
	// A form-action restriction also blocks redirects to the client's OAuth callback.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src "+mcpConsentStyleCSP+"; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	// Preserve Origin on the same-origin form POST while hiding cross-origin referrers.
	w.Header().Set("Referrer-Policy", "same-origin")
	if r.Method == http.MethodPost {
		s.consent(w, r)
		return
	}
	if r.Method != http.MethodGet {
		mcpError(w, 405, "invalid_request")
		return
	}
	q := r.URL.Query()
	if !mcpSingleValues(q) || len(r.URL.RawQuery) > 8192 {
		mcpError(w, 400, "invalid_request")
		return
	}
	s.mu.Lock()
	s.prune()
	id := q.Get("request")
	var pending *mcpAuthorization
	if id != "" {
		pending = s.pending[mcpHash(id)]
	} else {
		client, ok := s.clients[q.Get("client_id")]
		challenge, err := base64.RawURLEncoding.DecodeString(q.Get("code_challenge"))
		if !ok || !mcpRedirectAllowed(client.RedirectURIs, q.Get("redirect_uri")) || q.Get("response_type") != "code" ||
			q.Get("code_challenge_method") != "S256" || err != nil || len(challenge) != 32 || !s.validResource(q.Get("resource")) ||
			(q.Get("scope") != "" && q.Get("scope") != "mcp") {
			s.mu.Unlock()
			mcpError(w, 400, "invalid_request")
			return
		}
		pendingForClient := 0
		for _, request := range s.pending {
			if request.ClientID == client.ID {
				pendingForClient++
			}
		}
		if pendingForClient >= mcpPendingPerClient {
			s.mu.Unlock()
			mcpError(w, http.StatusTooManyRequests, "temporarily_unavailable")
			return
		}
		if len(s.pending) >= mcpStoreLimit {
			s.mu.Unlock()
			mcpError(w, 503, "temporarily_unavailable")
			return
		}
		id = mcpRandom()
		pending = &mcpAuthorization{ClientID: client.ID, RedirectURI: q.Get("redirect_uri"), Resource: q.Get("resource"), State: q.Get("state"),
			Challenge: q.Get("code_challenge"), Expires: s.now().Add(mcpPendingTTL)}
		s.pending[mcpHash(id)] = pending
	}
	if pending == nil {
		s.mu.Unlock()
		mcpError(w, 400, "invalid_request")
		return
	}
	if _, ok := s.clients[pending.ClientID]; !ok {
		s.mu.Unlock()
		mcpError(w, 400, "invalid_client")
		return
	}
	session := s.browserSession(r)
	if session == nil {
		s.mu.Unlock()
		returnTo := s.basePath + "/auth/mcp/authorize?request=" + url.QueryEscape(id)
		http.Redirect(w, r, s.basePath+"/auth/login?return_to="+url.QueryEscape(returnTo), http.StatusFound)
		return
	}
	csrf := mcpRandom()
	pending.CSRF = mcpHash(csrf)
	pending.SID = session.SID
	client := s.clients[pending.ClientID]
	redirectHost, local := mcpLocalRedirect(pending.RedirectURI)
	data := map[string]any{"Username": session.User.Username, "ClientName": client.Name, "ClientID": client.ID, "Resource": pending.Resource,
		"RedirectURI": pending.RedirectURI, "RedirectHost": redirectHost, "ThirdParty": !local, "Lifetime": fmt.Sprintf("%d hours", int(mcpGrantTTL.Hours())),
		"Request": id, "CSRF": csrf, "Capability": "It may run read and write tools."}
	if strings.HasSuffix(pending.Resource, "/mcp-readonly") {
		data["Capability"] = "It exposes read-only tools."
	}
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: mcpConsentCookie + mcpHash(id), Value: csrf, Path: s.basePath + "/auth/mcp/authorize", Secure: strings.HasPrefix(s.origin, "https://"), HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 600})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = mcpConsentTemplate.Execute(w, data)
}

func (s *MCPOAuthServer) consent(w http.ResponseWriter, r *http.Request) {
	origin, ok := urlutil.NormalizeOrigin(r.Header.Get("Origin"))
	expectedOrigin, _ := urlutil.NormalizeOrigin(s.origin)
	if !ok || origin != expectedOrigin {
		mcpError(w, 400, "invalid_request")
		return
	}
	if !mcpParseForm(w, r) {
		return
	}
	session := s.browserSession(r)
	id := mcpHash(r.PostForm.Get("request"))
	cookie, err := r.Cookie(mcpConsentCookie + id)
	if session == nil || err != nil {
		mcpError(w, 403, "access_denied")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune()
	pending := s.pending[id]
	if pending == nil || pending.SID != session.SID || pending.CSRF == "" || pending.CSRF != mcpHash(cookie.Value) || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(r.PostForm.Get("csrf"))) != 1 {
		mcpError(w, 403, "access_denied")
		return
	}
	if r.PostForm.Get("decision") != "allow" && r.PostForm.Get("decision") != "deny" {
		mcpError(w, 400, "invalid_request")
		return
	}
	client, ok := s.clients[pending.ClientID]
	if !ok {
		mcpError(w, 400, "invalid_client")
		return
	}
	if len(s.codes) >= mcpStoreLimit {
		mcpError(w, 503, "temporarily_unavailable")
		return
	}
	delete(s.pending, id)
	http.SetCookie(w, &http.Cookie{Name: mcpConsentCookie + id, Path: s.basePath + "/auth/mcp/authorize", MaxAge: -1, Secure: strings.HasPrefix(s.origin, "https://"), HttpOnly: true, SameSite: http.SameSiteLaxMode})
	u, _ := url.Parse(pending.RedirectURI)
	q := u.Query()
	if pending.State != "" {
		q.Set("state", pending.State)
	}
	returnHost, _ := mcpLocalRedirect(pending.RedirectURI)
	fields := mcpAuditFields{User: session.User.Username, ClientID: client.ID, ClientName: client.Name, ReturnHost: returnHost, Resource: pending.Resource}
	if r.PostForm.Get("decision") == "deny" {
		q.Set("error", "access_denied")
		fields.Reason = "user denied consent"
		mcpAuditLog("access denied", fields)
	} else {
		client.Expires = s.now().Add(mcpClientTTL)
		s.clients[client.ID] = client
		code := mcpRandom()
		grant := &mcpGrant{ClientID: pending.ClientID, Resource: pending.Resource, SID: session.SID, ReturnHost: returnHost,
			User: User{Username: session.User.Username, Groups: slices.Clone(session.User.Groups)}, Expires: s.now().Add(mcpGrantTTL)}
		s.codes[mcpHash(code)] = &mcpCode{mcpAuthorization: *pending, Grant: grant}
		s.codes[mcpHash(code)].Expires = s.now().Add(time.Minute)
		q.Set("code", code)
		mcpAuditLog("access approved", fields)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

func (s *MCPOAuthServer) browserSession(r *http.Request) *Session {
	session := ParseSessionCookie(r, s.cfg.Secret)
	if session == nil || session.SID == "" || session.User == nil || !ForwardedIdentityAllowed(session.User.Username, session.User.Groups, false) ||
		(s.cfg.Revoker != nil && s.cfg.Revoker.IsRevoked(session.SID)) {
		return nil
	}
	return session
}

// HandleToken exchanges one-time codes or rotates refresh tokens.
func (s *MCPOAuthServer) HandleToken(w http.ResponseWriter, r *http.Request) {
	if !mcpParseForm(w, r) {
		return
	}
	if r.Header.Get("Authorization") != "" {
		mcpError(w, 400, "invalid_request")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune()
	clientID := r.PostForm.Get("client_id")
	if _, ok := s.clients[clientID]; !ok {
		mcpError(w, 400, "invalid_client")
		return
	}
	var grant *mcpGrant
	event := "tokens issued"
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		code := s.codes[mcpHash(r.PostForm.Get("code"))]
		verifier := r.PostForm.Get("code_verifier")
		if code == nil || code.ClientID != clientID || code.RedirectURI != r.PostForm.Get("redirect_uri") || code.Resource != r.PostForm.Get("resource") || !mcpVerifier(verifier) || mcpHash(verifier) != code.Challenge {
			mcpError(w, 400, "invalid_grant")
			return
		}
		if code.Used {
			if !code.Grant.Revoked {
				s.auditGrant("replay detected, grant revoked", code.Grant, "authorization code reused")
			}
			code.Grant.Revoked = true
			mcpError(w, 400, "invalid_grant")
			return
		}
		if len(s.refresh) >= mcpStoreLimit {
			mcpError(w, 503, "temporarily_unavailable")
			return
		}
		code.Used = true
		grant = code.Grant
		grant.RefreshID = mcpRandom()
	case "refresh_token":
		raw := r.PostForm.Get("refresh_token")
		token := s.refresh[s.refreshFamily(raw)]
		if token == nil || token.Grant.ClientID != clientID || (r.PostForm.Get("resource") != "" && token.Grant.Resource != r.PostForm.Get("resource")) || (r.PostForm.Get("scope") != "" && r.PostForm.Get("scope") != "mcp") {
			mcpError(w, 400, "invalid_grant")
			return
		}
		if token.RefreshHash != mcpHash(raw) {
			if !token.Grant.Revoked {
				s.auditGrant("replay detected, grant revoked", token.Grant, "superseded refresh token reused")
			}
			token.Grant.Revoked = true
			mcpError(w, 400, "invalid_grant")
			return
		}
		grant = token.Grant
		event = "tokens refreshed"
	default:
		mcpError(w, 400, "unsupported_grant_type")
		return
	}
	if !s.validGrant(grant) {
		mcpError(w, 400, "invalid_grant")
		return
	}
	access := mcpRandom()
	refresh := grant.RefreshID + "." + mcpRandom()
	refresh += "." + s.refreshSignature(refresh)
	expires := s.now().Add(mcpAccessTTL)
	if grant.Expires.Before(expires) {
		expires = grant.Expires
	}
	delete(s.access, grant.AccessHash)
	grant.AccessHash = mcpHash(access)
	s.access[grant.AccessHash] = &mcpToken{Grant: grant, Expires: expires}
	s.refresh[grant.RefreshID] = &mcpToken{Grant: grant, Expires: grant.Expires, RefreshHash: mcpHash(refresh)}
	s.auditGrant(event, grant, "")
	mcpJSON(w, 200, map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": int(expires.Sub(s.now()).Seconds()), "refresh_token": refresh, "scope": "mcp"})
}

// HandleRevoke invalidates the entire authorization grant containing a token.
func (s *MCPOAuthServer) HandleRevoke(w http.ResponseWriter, r *http.Request) {
	if !mcpParseForm(w, r) {
		return
	}
	if r.Header.Get("Authorization") != "" {
		mcpError(w, 400, "invalid_request")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune()
	raw := r.PostForm.Get("token")
	for _, token := range []*mcpToken{s.access[mcpHash(raw)], s.refresh[s.refreshFamily(raw)]} {
		if token != nil && token.Grant.ClientID == r.PostForm.Get("client_id") {
			if !token.Grant.Revoked {
				s.auditGrant("grant revoked", token.Grant, "revoked by the client")
			}
			token.Grant.Revoked = true
		}
	}
	mcpPrivateHeaders(w)
	w.WriteHeader(http.StatusOK)
}

func (s *MCPOAuthServer) refreshSignature(value string) string {
	mac := hmac.New(sha256.New, []byte(s.cfg.Secret))
	mac.Write([]byte("radar-mcp-refresh\x00" + value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *MCPOAuthServer) refreshFamily(value string) string {
	parts := strings.Split(value, ".")
	if len(parts) != 3 || len(parts[0]) != 43 || len(parts[1]) != 43 || len(parts[2]) != 43 {
		return ""
	}
	if !hmac.Equal([]byte(parts[2]), []byte(s.refreshSignature(parts[0]+"."+parts[1]))) {
		return ""
	}
	// Authentic older nonces identify a replay without retaining token history.
	return parts[0]
}

// RevokeSession invalidates grants permanently, beyond the browser revoker's TTL.
// The reason names the logout that triggered it for the audit log.
func (s *MCPOAuthServer) RevokeSession(sid, reason string) {
	if sid == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, pending := range s.pending {
		if pending.SID == sid {
			delete(s.pending, id)
		}
	}
	revoke := func(grant *mcpGrant) {
		if grant.SID == sid && !grant.Revoked {
			grant.Revoked = true
			s.auditGrant("grant revoked", grant, reason)
		}
	}
	for _, code := range s.codes {
		revoke(code.Grant)
	}
	for _, tokens := range []map[string]*mcpToken{s.access, s.refresh} {
		for _, token := range tokens {
			revoke(token.Grant)
		}
	}
}

type mcpAuditFields struct {
	User, ClientID, ClientName, ReturnHost, Resource, Reason string
}

// mcpAuditLog records the grant lifecycle. Callers pass only identities and
// hosts: never token, code, verifier or secret values, nor a redirect URI,
// whose query string may carry client state.
func mcpAuditLog(event string, f mcpAuditFields) {
	line := "[auth] MCP " + event + ":"
	if f.User != "" {
		line += fmt.Sprintf(" user=%q", f.User)
	}
	line += fmt.Sprintf(" client_id=%q client_name_self_reported=%q", f.ClientID, f.ClientName)
	if f.ReturnHost != "" {
		line += fmt.Sprintf(" return_host=%q", f.ReturnHost)
	}
	if f.Resource != "" {
		endpoint := "full"
		if strings.HasSuffix(f.Resource, "/mcp-readonly") {
			endpoint = "read-only"
		}
		line += fmt.Sprintf(" endpoint=%q", endpoint)
	}
	if f.Reason != "" {
		line += fmt.Sprintf(" reason=%q", f.Reason)
	}
	log.Print(line)
}

// auditGrant must be called with s.mu held.
func (s *MCPOAuthServer) auditGrant(event string, grant *mcpGrant, reason string) {
	mcpAuditLog(event, mcpAuditFields{User: grant.User.Username, ClientID: grant.ClientID, ClientName: s.clients[grant.ClientID].Name,
		ReturnHost: grant.ReturnHost, Resource: grant.Resource, Reason: reason})
}

// Authenticate authenticates MCP bearer tokens, retaining browser cookie support.
func (s *MCPOAuthServer) Authenticate(next http.Handler) http.Handler {
	cookieAuth := Authenticate(s.cfg)(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resource, ok := s.resources[r.URL.Path]
		if !ok {
			mcpError(w, 401, "invalid_token")
			return
		}
		challenge := `Bearer resource_metadata="` + s.issuer + "/.well-known/oauth-protected-resource" + r.URL.Path + `", scope="mcp"`
		header := r.Header.Get("Authorization")
		if header == "" {
			if s.browserSession(r) == nil {
				w.Header().Set("WWW-Authenticate", challenge)
				mcpError(w, 401, "unauthorized")
				return
			}
			cookieAuth.ServeHTTP(w, r)
			return
		}
		parts := strings.Fields(header)
		s.mu.Lock()
		var user *User
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			token := s.access[mcpHash(parts[1])]
			if token != nil && token.Expires.After(s.now()) && token.Grant.Resource == resource && s.validGrant(token.Grant) {
				copy := token.Grant.User
				copy.Groups = slices.Clone(copy.Groups)
				user = &copy
			}
		}
		s.mu.Unlock()
		if user == nil {
			w.Header().Set("WWW-Authenticate", challenge+`, error="invalid_token"`)
			mcpError(w, 401, "invalid_token")
			return
		}
		next.ServeHTTP(w, r.WithContext(ContextWithUser(r.Context(), user)))
	})
}

func (s *MCPOAuthServer) validResource(resource string) bool {
	for _, allowed := range s.resources {
		if resource == allowed {
			return true
		}
	}
	return false
}

func (s *MCPOAuthServer) validGrant(grant *mcpGrant) bool {
	return !grant.Revoked && grant.Expires.After(s.now()) && ForwardedIdentityAllowed(grant.User.Username, grant.User.Groups, false) &&
		(s.cfg.Revoker == nil || !s.cfg.Revoker.IsRevoked(grant.SID))
}

func (s *MCPOAuthServer) prune() {
	now := s.now()
	for id, client := range s.clients {
		if !client.Expires.After(now) {
			delete(s.clients, id)
		}
	}
	for id, pending := range s.pending {
		if !pending.Expires.After(now) {
			delete(s.pending, id)
		}
	}
	for id, code := range s.codes {
		if !code.Expires.After(now) {
			delete(s.codes, id)
		}
	}
	for _, tokens := range []map[string]*mcpToken{s.access, s.refresh} {
		for id, token := range tokens {
			if !token.Expires.After(now) || token.Grant.Revoked {
				delete(tokens, id)
			}
		}
	}
}

func mcpParseForm(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost || strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/x-www-form-urlencoded" {
		mcpError(w, 400, "invalid_request")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	err := r.ParseForm()
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		mcpError(w, http.StatusRequestEntityTooLarge, "invalid_request")
		return false
	}
	if err != nil || !mcpSingleValues(r.PostForm) {
		mcpError(w, 400, "invalid_request")
		return false
	}
	return true
}

func mcpSingleValues(values url.Values) bool {
	for _, values := range values {
		if len(values) != 1 {
			return false
		}
	}
	return true
}

func mcpVerifier(value string) bool {
	if len(value) < 43 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~", c)) {
			return false
		}
	}
	return true
}

func mcpRandom() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func mcpHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func mcpPrivateHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}
func mcpError(w http.ResponseWriter, status int, code string) {
	mcpJSON(w, status, map[string]string{"error": code})
}
func mcpJSON(w http.ResponseWriter, status int, value any) {
	mcpPrivateHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
