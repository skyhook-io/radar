package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	mcpAccessTTL     = 10 * time.Minute
	mcpGrantTTL      = 24 * time.Hour
	mcpPendingTTL    = 10 * time.Minute
	mcpClientTTL     = 30 * 24 * time.Hour
	mcpStoreLimit    = 4096
	mcpConsentCookie = "radar_mcp_consent"
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
	Grant   *mcpGrant
	Expires time.Time
	Used    bool
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

// Issuer returns the public issuer derived from the configured OIDC callback.
func (s *MCPOAuthServer) Issuer() string { return s.issuer }

// MetadataPath returns the origin-relative RFC 8414 discovery path.
func (s *MCPOAuthServer) MetadataPath() string {
	return "/.well-known/oauth-authorization-server" + s.basePath
}

// HandleMetadata serves OAuth authorization server metadata.
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
	s.mu.Lock()
	if s.now().Sub(s.registrationWindow) >= time.Minute {
		s.registrationWindow = s.now()
		s.registrations = 0
	}
	s.registrations++
	limited := s.registrations > 20
	s.mu.Unlock()
	if limited {
		w.Header().Set("Retry-After", "60")
		mcpError(w, http.StatusTooManyRequests, "temporarily_unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var client mcpClient
	d := json.NewDecoder(r.Body)
	if d.Decode(&client) != nil || d.Decode(new(any)) != io.EOF || len(client.RedirectURIs) == 0 || len(client.RedirectURIs) > 10 || len(client.Name) > 100 {
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
		if err != nil || len(raw) > 2048 || u.Host == "" || u.User != nil || u.Fragment != "" || !mcpSecureURL(u) {
			mcpError(w, 400, "invalid_redirect_uri")
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune()
	if len(s.clients) >= 1024 {
		mcpError(w, 503, "temporarily_unavailable")
		return
	}
	client.ID = mcpRandom()
	client.AuthMethod = "none"
	client.GrantTypes = []string{"authorization_code", "refresh_token"}
	client.ResponseTypes = []string{"code"}
	// Unapproved registrations expire quickly so anonymous clients cannot fill
	// the long-lived client store without a user's consent.
	client.Expires = s.now().Add(mcpPendingTTL)
	s.clients[client.ID] = client
	mcpJSON(w, http.StatusCreated, client)
}

var mcpConsentTemplate = template.Must(template.New("consent").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Authorize Radar MCP</title></head><body><main><h1>Authorize Radar MCP</h1><p>Signed in as <strong>{{.Username}}</strong>.</p><p>Application name (provided by the application): <strong>{{.ClientName}}</strong></p><p>Client ID: <code>{{.ClientID}}</code></p><p>Allow this application to access <code>{{.Resource}}</code> with your Kubernetes permissions? {{.Capability}}</p><p>Authorization returns to <code>{{.RedirectURI}}</code>.</p><form method="post"><input type="hidden" name="request" value="{{.Request}}"><input type="hidden" name="csrf" value="{{.CSRF}}"><button name="decision" value="allow">Allow</button> <button name="decision" value="deny">Deny</button></form></main></body></html>`))

// HandleAuthorize validates authorization requests and requires explicit browser consent.
func (s *MCPOAuthServer) HandleAuthorize(w http.ResponseWriter, r *http.Request) {
	mcpPrivateHeaders(w)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
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
		if !ok || !slices.Contains(client.RedirectURIs, q.Get("redirect_uri")) || q.Get("response_type") != "code" ||
			q.Get("code_challenge_method") != "S256" || err != nil || len(challenge) != 32 || !s.validResource(q.Get("resource")) ||
			(q.Get("scope") != "" && q.Get("scope") != "mcp") {
			s.mu.Unlock()
			mcpError(w, 400, "invalid_request")
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
	data := map[string]string{"Username": session.User.Username, "ClientName": client.Name, "ClientID": client.ID, "Resource": pending.Resource,
		"RedirectURI": pending.RedirectURI, "Request": id, "CSRF": csrf, "Capability": "This endpoint may run read and write tools."}
	if strings.HasSuffix(pending.Resource, "/mcp-readonly") {
		data["Capability"] = "This endpoint exposes read-only tools."
	}
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: mcpConsentCookie, Value: csrf, Path: s.basePath + "/auth/mcp/authorize", Secure: strings.HasPrefix(s.origin, "https://"), HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 600})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = mcpConsentTemplate.Execute(w, data)
}

func (s *MCPOAuthServer) consent(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != s.origin || !mcpParseForm(w, r) {
		mcpError(w, 400, "invalid_request")
		return
	}
	session := s.browserSession(r)
	cookie, err := r.Cookie(mcpConsentCookie)
	if session == nil || err != nil {
		mcpError(w, 403, "access_denied")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune()
	id := mcpHash(r.PostForm.Get("request"))
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
	http.SetCookie(w, &http.Cookie{Name: mcpConsentCookie, Path: s.basePath + "/auth/mcp/authorize", MaxAge: -1, Secure: strings.HasPrefix(s.origin, "https://"), HttpOnly: true, SameSite: http.SameSiteLaxMode})
	u, _ := url.Parse(pending.RedirectURI)
	q := u.Query()
	if pending.State != "" {
		q.Set("state", pending.State)
	}
	if r.PostForm.Get("decision") == "deny" {
		q.Set("error", "access_denied")
	} else {
		client.Expires = s.now().Add(mcpClientTTL)
		s.clients[client.ID] = client
		code := mcpRandom()
		grant := &mcpGrant{ClientID: pending.ClientID, Resource: pending.Resource, SID: session.SID,
			User: User{Username: session.User.Username, Groups: slices.Clone(session.User.Groups)}, Expires: s.now().Add(mcpGrantTTL)}
		s.codes[mcpHash(code)] = &mcpCode{mcpAuthorization: *pending, Grant: grant}
		s.codes[mcpHash(code)].Expires = s.now().Add(time.Minute)
		q.Set("code", code)
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
	if !mcpParseForm(w, r) || r.Header.Get("Authorization") != "" {
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
	if len(s.access) >= mcpStoreLimit || len(s.refresh) >= mcpStoreLimit {
		mcpError(w, 503, "temporarily_unavailable")
		return
	}
	var grant *mcpGrant
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		code := s.codes[mcpHash(r.PostForm.Get("code"))]
		verifier := r.PostForm.Get("code_verifier")
		if code == nil || code.ClientID != clientID || code.RedirectURI != r.PostForm.Get("redirect_uri") || code.Resource != r.PostForm.Get("resource") || !mcpVerifier(verifier) || mcpHash(verifier) != code.Challenge {
			mcpError(w, 400, "invalid_grant")
			return
		}
		if code.Used {
			code.Grant.Revoked = true
			mcpError(w, 400, "invalid_grant")
			return
		}
		code.Used = true
		grant = code.Grant
	case "refresh_token":
		token := s.refresh[mcpHash(r.PostForm.Get("refresh_token"))]
		if token == nil || token.Grant.ClientID != clientID || (r.PostForm.Get("resource") != "" && token.Grant.Resource != r.PostForm.Get("resource")) || (r.PostForm.Get("scope") != "" && r.PostForm.Get("scope") != "mcp") {
			mcpError(w, 400, "invalid_grant")
			return
		}
		if token.Used {
			token.Grant.Revoked = true
			mcpError(w, 400, "invalid_grant")
			return
		}
		token.Used = true
		grant = token.Grant
	default:
		mcpError(w, 400, "unsupported_grant_type")
		return
	}
	if !s.validGrant(grant) {
		mcpError(w, 400, "invalid_grant")
		return
	}
	access, refresh := mcpRandom(), mcpRandom()
	expires := s.now().Add(mcpAccessTTL)
	if grant.Expires.Before(expires) {
		expires = grant.Expires
	}
	s.access[mcpHash(access)] = &mcpToken{Grant: grant, Expires: expires}
	s.refresh[mcpHash(refresh)] = &mcpToken{Grant: grant, Expires: grant.Expires}
	mcpJSON(w, 200, map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": int(expires.Sub(s.now()).Seconds()), "refresh_token": refresh, "scope": "mcp"})
}

// HandleRevoke invalidates the entire authorization grant containing a token.
func (s *MCPOAuthServer) HandleRevoke(w http.ResponseWriter, r *http.Request) {
	if !mcpParseForm(w, r) || r.Header.Get("Authorization") != "" {
		mcpError(w, 400, "invalid_request")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune()
	key := mcpHash(r.PostForm.Get("token"))
	for _, tokens := range []map[string]*mcpToken{s.access, s.refresh} {
		if token := tokens[key]; token != nil && token.Grant.ClientID == r.PostForm.Get("client_id") {
			token.Grant.Revoked = true
		}
	}
	mcpPrivateHeaders(w)
	w.WriteHeader(http.StatusOK)
}

// RevokeSession invalidates grants permanently, beyond the browser revoker's TTL.
func (s *MCPOAuthServer) RevokeSession(sid string) {
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
	for _, code := range s.codes {
		if code.Grant.SID == sid {
			code.Grant.Revoked = true
		}
	}
	for _, tokens := range []map[string]*mcpToken{s.access, s.refresh} {
		for _, token := range tokens {
			if token.Grant.SID == sid {
				token.Grant.Revoked = true
			}
		}
	}
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
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	return r.ParseForm() == nil && mcpSingleValues(r.PostForm)
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
