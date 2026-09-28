package auth

import (
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/coreos/go-oidc/v3/oidc/oidctest"
)

type mcpOAuthFixture struct {
	t        *testing.T
	s        *MCPOAuthServer
	client   mcpClient
	cookies  []*http.Cookie
	verifier string
}

func newMCPOAuthFixture(t *testing.T) *mcpOAuthFixture {
	t.Helper()
	cfg := Config{Mode: "oidc", Secret: "test-secret", OIDCRedirectURL: "https://radar.example/radar/auth/callback"}
	s, err := NewMCPOAuthServer(cfg, "/radar", []string{"/mcp", "/mcp-readonly"})
	if err != nil {
		t.Fatal(err)
	}
	f := &mcpOAuthFixture{t: t, s: s, verifier: strings.Repeat("v", 43)}
	f.cookies = CreateSessionCookie(&User{Username: "alice@example.com", Groups: []string{"developers"}}, "browser-sid", "", cfg.Secret, time.Hour, true)
	r := httptest.NewRequest("POST", "/auth/mcp/register", strings.NewReader(`{"client_name":"Example MCP client","redirect_uris":["http://127.0.0.1:54321/callback"]}`))
	w := httptest.NewRecorder()
	s.HandleRegister(w, r)
	if w.Code != 201 {
		t.Fatalf("registration: %d %s", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &f.client); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *mcpOAuthFixture) request(resource string) url.Values {
	return url.Values{"response_type": {"code"}, "client_id": {f.client.ID}, "redirect_uri": {f.client.RedirectURIs[0]},
		"resource": {f.s.issuer + resource}, "scope": {"mcp"}, "state": {"client-state"}, "code_challenge": {mcpHash(f.verifier)}, "code_challenge_method": {"S256"}}
}

func (f *mcpOAuthFixture) authorize(query url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest("GET", "/auth/mcp/authorize?"+query.Encode(), nil)
	for _, cookie := range cookies {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	f.s.HandleAuthorize(w, r)
	return w
}

func (f *mcpOAuthFixture) consentForm(page *httptest.ResponseRecorder) url.Values {
	f.t.Helper()
	if page.Code != 200 {
		f.t.Fatalf("consent page: %d %s", page.Code, page.Body)
	}
	values := url.Values{"decision": {"allow"}}
	for _, field := range []string{"request", "csrf"} {
		match := regexp.MustCompile(`name="` + field + `" value="([^"]+)"`).FindStringSubmatch(page.Body.String())
		if len(match) != 2 {
			f.t.Fatalf("missing field %s", field)
		}
		values.Set(field, html.UnescapeString(match[1]))
	}
	return values
}

func (f *mcpOAuthFixture) consent(page *httptest.ResponseRecorder, form url.Values, origin string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest("POST", "/auth/mcp/authorize", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", origin)
	for _, cookie := range cookies {
		r.AddCookie(cookie)
	}
	for _, cookie := range page.Result().Cookies() {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	f.s.HandleAuthorize(w, r)
	return w
}

func (f *mcpOAuthFixture) code(resource string) string {
	f.t.Helper()
	page := f.authorize(f.request(resource), f.cookies)
	w := f.consent(page, f.consentForm(page), f.s.origin, f.cookies)
	if w.Code != 303 {
		f.t.Fatalf("consent: %d %s", w.Code, w.Body)
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil || u.Query().Get("state") != "client-state" || u.Query().Get("code") == "" {
		f.t.Fatalf("callback: %s %v", u, err)
	}
	return u.Query().Get("code")
}

func (f *mcpOAuthFixture) exchangeForm(code, resource string) url.Values {
	return url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {f.client.ID}, "redirect_uri": {f.client.RedirectURIs[0]}, "resource": {f.s.issuer + resource}, "code_verifier": {f.verifier}}
}

func (f *mcpOAuthFixture) token(form url.Values) (*httptest.ResponseRecorder, map[string]any) {
	f.t.Helper()
	r := httptest.NewRequest("POST", "/auth/mcp/token", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	f.s.HandleToken(w, r)
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		f.t.Fatal(err)
	}
	return w, body
}

func (f *mcpOAuthFixture) access(token, path string) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest("POST", path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	f.s.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := UserFromContext(r.Context())
		if user == nil || user.Username != "alice@example.com" || len(user.Groups) != 1 || user.Groups[0] != "developers" {
			f.t.Errorf("identity not preserved: %+v", user)
		}
		w.WriteHeader(204)
	})).ServeHTTP(w, r)
	return w
}

func TestMCPOAuthDiscoveryAndLoginContinuation(t *testing.T) {
	f := newMCPOAuthFixture(t)
	w := f.access("", "/mcp-readonly")
	if w.Code != 401 || !strings.Contains(w.Header().Get("WWW-Authenticate"), `resource_metadata="https://radar.example/radar/.well-known/oauth-protected-resource/mcp-readonly"`) {
		t.Fatalf("challenge: %d %v", w.Code, w.Header())
	}
	metadata := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "https://attacker.invalid/.well-known/oauth-authorization-server", nil)
	r.Header.Set("X-Forwarded-Host", "attacker.invalid")
	f.s.HandleMetadata(metadata, r)
	if strings.Contains(metadata.Body.String(), "attacker") || !strings.Contains(metadata.Body.String(), `"issuer":"https://radar.example/radar"`) {
		t.Fatalf("untrusted metadata origin: %s", metadata.Body)
	}
	resource := httptest.NewRecorder()
	f.s.ProtectedResourceMetadata("/mcp-readonly")(resource, r)
	if !strings.Contains(resource.Body.String(), `"resource":"https://radar.example/radar/mcp-readonly"`) {
		t.Fatal(resource.Body)
	}
	w = f.authorize(f.request("/mcp-readonly"), nil)
	location, err := url.Parse(w.Header().Get("Location"))
	if err != nil || w.Code != 302 || location.Path != "/radar/auth/login" {
		t.Fatalf("login redirect: %d %s", w.Code, location)
	}
	continuation, err := url.Parse(location.Query().Get("return_to"))
	if err != nil || continuation.Path != "/radar/auth/mcp/authorize" || continuation.Query().Get("request") == "" || len(continuation.Query()) != 1 {
		t.Fatalf("unsafe continuation: %v", continuation)
	}
	page := f.authorize(continuation.Query(), f.cookies)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "read-only tools") {
		t.Fatalf("resume failed: %d %s", page.Code, page.Body)
	}
}

func TestMCPOAuthCodePKCEAndAudienceIsolation(t *testing.T) {
	f := newMCPOAuthFixture(t)
	code := f.code("/mcp-readonly")
	for name, change := range map[string]func(url.Values){
		"wrong verifier": func(v url.Values) { v.Set("code_verifier", strings.Repeat("w", 43)) },
		"wrong client":   func(v url.Values) { v.Set("client_id", "other") },
		"wrong redirect": func(v url.Values) { v.Set("redirect_uri", "http://127.0.0.1:54322/callback") },
		"wrong resource": func(v url.Values) { v.Set("resource", f.s.issuer+"/mcp") },
	} {
		t.Run(name, func(t *testing.T) {
			form := f.exchangeForm(code, "/mcp-readonly")
			change(form)
			w, _ := f.token(form)
			if w.Code != 400 {
				t.Fatalf("accepted: %d", w.Code)
			}
		})
	}
	w, token := f.token(f.exchangeForm(code, "/mcp-readonly"))
	if w.Code != 200 || token["token_type"] != "Bearer" {
		t.Fatalf("exchange: %d %v", w.Code, token)
	}
	access := token["access_token"].(string)
	if got := f.access(access, "/mcp-readonly").Code; got != 204 {
		t.Fatalf("access: %d", got)
	}
	for _, path := range []string{"/mcp", "/mcp-investigation", "/api/resources/pods", "/mcp-readonly/"} {
		if got := f.access(access, path).Code; got != 401 {
			t.Fatalf("token accepted for %s: %d", path, got)
		}
	}
	if got := f.access(token["refresh_token"].(string), "/mcp-readonly").Code; got != 401 {
		t.Fatalf("refresh accepted as access: %d", got)
	}
	w, _ = f.token(f.exchangeForm(code, "/mcp-readonly"))
	if w.Code != 400 || f.access(access, "/mcp-readonly").Code != 401 {
		t.Fatal("code replay did not revoke grant")
	}
}

func TestMCPOAuthRefreshRotationAndRevocation(t *testing.T) {
	f := newMCPOAuthFixture(t)
	_, first := f.token(f.exchangeForm(f.code("/mcp"), "/mcp"))
	refresh := url.Values{"grant_type": {"refresh_token"}, "client_id": {f.client.ID}, "refresh_token": {first["refresh_token"].(string)}}
	refresh.Set("resource", f.s.issuer+"/mcp-readonly")
	if w, _ := f.token(refresh); w.Code != 400 {
		t.Fatal("refresh widened audience")
	}
	refresh.Del("resource")
	w, second := f.token(refresh)
	if w.Code != 200 || second["refresh_token"] == first["refresh_token"] {
		t.Fatalf("rotation: %d %v", w.Code, second)
	}
	if f.access(second["access_token"].(string), "/mcp").Code != 204 {
		t.Fatal("rotated access rejected")
	}
	if f.access(first["access_token"].(string), "/mcp").Code != 401 {
		t.Fatal("rotation retained the previous access token")
	}
	if w, _ := f.token(refresh); w.Code != 400 {
		t.Fatal("refresh replay accepted")
	}
	if f.access(first["access_token"].(string), "/mcp").Code != 401 || f.access(second["access_token"].(string), "/mcp").Code != 401 {
		t.Fatal("refresh replay did not revoke entire family")
	}
	refresh.Set("refresh_token", second["refresh_token"].(string))
	if w, _ := f.token(refresh); w.Code != 400 {
		t.Fatal("revoked family refreshed")
	}
}

func TestMCPOAuthRefreshStorageRemainsBoundedForFullGrantLifetime(t *testing.T) {
	f := newMCPOAuthFixture(t)
	now := time.Now()
	f.s.now = func() time.Time { return now }
	const families = 40
	refreshTokens := make([]string, families)
	for i := range refreshTokens {
		w, token := f.token(f.exchangeForm(f.code("/mcp"), "/mcp"))
		if w.Code != 200 {
			t.Fatalf("initial grant %d: %d %v", i, w.Code, token)
		}
		refreshTokens[i] = token["refresh_token"].(string)
	}
	firstRefresh := refreshTokens[0]
	for rotation := 1; rotation < 144; rotation++ {
		now = now.Add(mcpAccessTTL)
		for i, previous := range refreshTokens {
			form := url.Values{"grant_type": {"refresh_token"}, "client_id": {f.client.ID}, "refresh_token": {previous}}
			w, token := f.token(form)
			if w.Code != 200 {
				t.Fatalf("rotation %d grant %d: %d %v", rotation, i, w.Code, token)
			}
			refreshTokens[i] = token["refresh_token"].(string)
		}
		if len(f.s.refresh) != families || len(f.s.access) != families {
			t.Fatalf("rotation %d retained history: refresh=%d access=%d", rotation, len(f.s.refresh), len(f.s.access))
		}
	}
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {f.client.ID}, "refresh_token": {firstRefresh}}
	if w, _ := f.token(form); w.Code != 400 {
		t.Fatal("refresh replay after 23 hours 50 minutes was accepted")
	}
	form.Set("refresh_token", refreshTokens[0])
	if w, _ := f.token(form); w.Code != 400 {
		t.Fatal("old refresh replay did not revoke the current token")
	}
	now = now.Add(mcpAccessTTL)
	form.Set("refresh_token", refreshTokens[1])
	if w, _ := f.token(form); w.Code != 400 {
		t.Fatal("refresh extended the 24-hour grant lifetime")
	}
	if len(f.s.refresh) != 0 || len(f.s.access) != 0 {
		t.Fatal("expired grant storage was retained")
	}
}

func TestMCPOAuthRefreshAtCapacityAndRetryUnconsumedCode(t *testing.T) {
	f := newMCPOAuthFixture(t)
	_, token := f.token(f.exchangeForm(f.code("/mcp"), "/mcp"))
	code := f.code("/mcp")
	var spare *mcpGrant
	for range mcpStoreLimit - 1 {
		spare = &mcpGrant{ClientID: f.client.ID, Resource: f.s.issuer + "/mcp", Expires: f.s.now().Add(mcpGrantTTL),
			AccessHash: mcpRandom(), RefreshID: mcpRandom()}
		f.s.access[spare.AccessHash] = &mcpToken{Grant: spare, Expires: f.s.now().Add(mcpAccessTTL)}
		f.s.refresh[spare.RefreshID] = &mcpToken{Grant: spare, Expires: spare.Expires}
	}
	if w, _ := f.token(f.exchangeForm(code, "/mcp")); w.Code != 503 {
		t.Fatalf("new grant at capacity: %d", w.Code)
	}
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {f.client.ID}, "refresh_token": {token["refresh_token"].(string)}}
	w, renewed := f.token(form)
	if w.Code != 200 || len(f.s.refresh) != mcpStoreLimit || len(f.s.access) != mcpStoreLimit {
		t.Fatalf("existing refresh at capacity: %d refresh=%d access=%d", w.Code, len(f.s.refresh), len(f.s.access))
	}
	if f.access(renewed["access_token"].(string), "/mcp").Code != 204 {
		t.Fatal("rotated access token at capacity was rejected")
	}
	spare.Revoked = true
	if w, _ := f.token(f.exchangeForm(code, "/mcp")); w.Code != 200 {
		t.Fatalf("capacity rejection consumed the authorization code: %d", w.Code)
	}
}

func TestMCPOAuthMalformedRefreshCannotRevokeFamily(t *testing.T) {
	f := newMCPOAuthFixture(t)
	_, token := f.token(f.exchangeForm(f.code("/mcp"), "/mcp"))
	refresh := token["refresh_token"].(string)
	parts := strings.Split(refresh, ".")
	for _, malformed := range []string{parts[0], parts[0] + "." + parts[1], parts[0] + "." + mcpRandom() + "." + parts[2], parts[0] + "." + parts[1] + "." + mcpRandom()} {
		form := url.Values{"grant_type": {"refresh_token"}, "client_id": {f.client.ID}, "refresh_token": {malformed}}
		if w, _ := f.token(form); w.Code != 400 {
			t.Fatal("malformed refresh token was accepted")
		}
		form = url.Values{"client_id": {f.client.ID}, "token": {malformed}}
		r := httptest.NewRequest("POST", "/auth/mcp/revoke", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		f.s.HandleRevoke(w, r)
		if w.Code != 200 || f.access(token["access_token"].(string), "/mcp").Code != 204 {
			t.Fatal("malformed refresh token revoked the legitimate family")
		}
	}
	otherClient := f.s.clients[f.client.ID]
	otherClient.ID = "another-registered-client"
	f.s.clients[otherClient.ID] = otherClient
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {otherClient.ID}, "refresh_token": {refresh}}
	if w, _ := f.token(form); w.Code != 400 || f.access(token["access_token"].(string), "/mcp").Code != 204 {
		t.Fatal("wrong client revoked the legitimate family")
	}
	form.Set("client_id", f.client.ID)
	w, renewed := f.token(form)
	if w.Code != 200 {
		t.Fatal("invalid refresh attempts consumed the legitimate token")
	}
	form = url.Values{"client_id": {f.client.ID}, "token": {refresh}}
	r := httptest.NewRequest("POST", "/auth/mcp/revoke", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	f.s.HandleRevoke(w, r)
	if w.Code != 200 || f.access(renewed["access_token"].(string), "/mcp").Code != 401 {
		t.Fatal("authentic previously rotated refresh token could not revoke its family")
	}
}

func TestMCPOAuthConsentRejectsCSRFAndDenial(t *testing.T) {
	for _, scenario := range []string{"missing origin", "foreign origin", "wrong csrf", "wrong session", "deny"} {
		t.Run(scenario, func(t *testing.T) {
			f := newMCPOAuthFixture(t)
			page := f.authorize(f.request("/mcp"), f.cookies)
			form := f.consentForm(page)
			origin, cookies := f.s.origin, f.cookies
			switch scenario {
			case "missing origin":
				origin = ""
			case "foreign origin":
				origin = "https://attacker.invalid"
			case "wrong csrf":
				form.Set("csrf", "wrong")
			case "wrong session":
				cookies = CreateSessionCookie(&User{Username: "bob"}, "another-sid", "", f.s.cfg.Secret, time.Hour, true)
			case "deny":
				form.Set("decision", "deny")
			}
			w := f.consent(page, form, origin, cookies)
			if scenario == "deny" {
				location, _ := url.Parse(w.Header().Get("Location"))
				if w.Code != 303 || location.Query().Get("error") != "access_denied" || location.Query().Get("code") != "" {
					t.Fatalf("denial: %d %s", w.Code, location)
				}
			} else if w.Code != 400 && w.Code != 403 {
				t.Fatalf("unsafe consent accepted: %d", w.Code)
			}
			if len(f.s.codes) != 0 {
				t.Fatal("consent issued a code")
			}
		})
	}
}

func TestMCPOAuthRejectsUnsafeAuthorizationAndRegistration(t *testing.T) {
	f := newMCPOAuthFixture(t)
	for field, value := range map[string]string{"resource": "https://attacker.invalid/mcp", "redirect_uri": "https://attacker.invalid/callback", "code_challenge_method": "plain", "code_challenge": "short", "scope": "admin", "response_type": "token"} {
		t.Run(field, func(t *testing.T) {
			q := f.request("/mcp")
			q.Set(field, value)
			w := f.authorize(q, f.cookies)
			if w.Code != 400 || w.Header().Get("Location") != "" {
				t.Fatalf("unsafe redirect: %d %v", w.Code, w.Header())
			}
		})
	}
	for _, uri := range []string{"http://example.com/callback", "https://user:pass@example.com/callback", "https://example.com/callback#fragment", "javascript:alert(1)", "http://127.0.0.1.attacker.invalid/callback"} {
		t.Run(uri, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"redirect_uris": []string{uri}})
			r := httptest.NewRequest("POST", "/auth/mcp/register", strings.NewReader(string(body)))
			w := httptest.NewRecorder()
			f.s.HandleRegister(w, r)
			if w.Code != 400 {
				t.Fatalf("unsafe registration accepted: %d", w.Code)
			}
		})
	}
	q := f.request("/mcp")
	q.Add("resource", f.s.issuer+"/mcp-readonly")
	if f.authorize(q, f.cookies).Code != 400 {
		t.Fatal("duplicate authorization parameter accepted")
	}
}

func TestMCPOAuthExpiryAndSessionRevocation(t *testing.T) {
	f := newMCPOAuthFixture(t)
	revoker := NewMemoryRevoker()
	defer revoker.Stop()
	f.s.cfg.Revoker = revoker
	_, token := f.token(f.exchangeForm(f.code("/mcp"), "/mcp"))
	now := time.Now()
	f.s.now = func() time.Time { return now.Add(11 * time.Minute) }
	if f.access(token["access_token"].(string), "/mcp").Code != 401 {
		t.Fatal("expired access accepted")
	}
	refresh := url.Values{"grant_type": {"refresh_token"}, "client_id": {f.client.ID}, "refresh_token": {token["refresh_token"].(string)}}
	w, renewed := f.token(refresh)
	if w.Code != 200 {
		t.Fatalf("refresh within session failed: %d", w.Code)
	}
	revoker.Revoke("browser-sid", now.Add(time.Hour))
	if f.access(renewed["access_token"].(string), "/mcp").Code != 401 {
		t.Fatal("revoked browser session accepted")
	}
	refresh.Set("refresh_token", renewed["refresh_token"].(string))
	if w, _ := f.token(refresh); w.Code != 400 {
		t.Fatal("revoked browser session refreshed")
	}
	f.s.now = func() time.Time { return now.Add(25 * time.Hour) }
	f.s.prune()
	if len(f.s.access) != 0 || len(f.s.refresh) != 0 || len(f.s.codes) != 0 {
		t.Fatal("expired state was not pruned")
	}
}

func TestMCPOAuthRevokeEndpointAndReservedIdentity(t *testing.T) {
	f := newMCPOAuthFixture(t)
	_, token := f.token(f.exchangeForm(f.code("/mcp"), "/mcp"))
	form := url.Values{"client_id": {f.client.ID}, "token": {token["refresh_token"].(string)}}
	r := httptest.NewRequest("POST", "/auth/mcp/revoke", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	f.s.HandleRevoke(w, r)
	if w.Code != 200 || f.access(token["access_token"].(string), "/mcp").Code != 401 {
		t.Fatal("revocation failed")
	}
	f.cookies = CreateSessionCookie(&User{Username: "system:admin", Groups: []string{"system:masters"}}, "reserved-sid", "", f.s.cfg.Secret, time.Hour, true)
	if page := f.authorize(f.request("/mcp"), f.cookies); page.Code == 200 {
		t.Fatal("reserved principal accepted for consent")
	}
}

func TestMCPOAuthConstructorRejectsUntrustedOrigin(t *testing.T) {
	for _, redirect := range []string{"http://radar.example/auth/callback", "https://radar.example/wrong", "https://user@radar.example/auth/callback", "https://radar.example/auth/callback?x=y"} {
		if _, err := NewMCPOAuthServer(Config{Mode: "oidc", Secret: "secret", OIDCRedirectURL: redirect}, "", []string{"/mcp"}); err == nil {
			t.Fatalf("accepted %s", redirect)
		}
	}
}

func TestMCPOAuthLogoutPermanentlyRevokesGrants(t *testing.T) {
	for _, backchannel := range []bool{false, true} {
		t.Run(map[bool]string{false: "browser", true: "backchannel"}[backchannel], func(t *testing.T) {
			f := newMCPOAuthFixture(t)
			_, token := f.token(f.exchangeForm(f.code("/mcp"), "/mcp"))
			unexchanged := f.code("/mcp")
			page := f.authorize(f.request("/mcp"), f.cookies)
			form := f.consentForm(page)
			h := &OIDCHandler{cfg: f.s.cfg}
			w := httptest.NewRecorder()
			if backchannel {
				idp := newFakeIDP(t, oidc.RS256)
				h = idp.newHandler(t, f.s.cfg)
				revoker := NewMemoryRevoker()
				defer revoker.Stop()
				f.s.cfg.Revoker = revoker
				h.SetRevoker(revoker)
				h.SetMCPOAuthServer(f.s)
				claims, _ := json.Marshal(map[string]any{"iss": idp.issuer, "aud": "radar", "sub": "alice", "sid": "browser-sid", "jti": "logout-1",
					"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "events": map[string]any{backchannelLogoutEventURI: map[string]any{}}})
				logoutToken := oidctest.SignIDToken(idp.signer, idp.keyID, idp.alg, string(claims))
				r := httptest.NewRequest("POST", "/auth/backchannel-logout", strings.NewReader(url.Values{"logout_token": {logoutToken}}.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				h.HandleBackchannelLogout(w, r)
				// Emulate the browser revocation record expiring while the MCP
				// authorization still has time left on its 24-hour lifetime.
				revoker.Revoke("browser-sid", time.Now().Add(-time.Second))
				if revoker.IsRevoked("browser-sid") {
					t.Fatal("revoker did not expire")
				}
			} else {
				h.SetMCPOAuthServer(f.s)
				r := httptest.NewRequest("GET", "/auth/logout", nil)
				for _, cookie := range f.cookies {
					r.AddCookie(cookie)
				}
				h.HandleLogout(w, r)
			}
			if w.Code != 200 {
				t.Fatalf("logout failed: %d %s", w.Code, w.Body)
			}
			if f.access(token["access_token"].(string), "/mcp").Code != 401 {
				t.Fatal("logged out access survived")
			}
			refresh := url.Values{"grant_type": {"refresh_token"}, "client_id": {f.client.ID}, "refresh_token": {token["refresh_token"].(string)}}
			if w, _ := f.token(refresh); w.Code != 400 {
				t.Fatal("logged out refresh survived")
			}
			if w, _ := f.token(f.exchangeForm(unexchanged, "/mcp")); w.Code != 400 {
				t.Fatal("logged out code survived")
			}
			if f.consent(page, form, f.s.origin, f.cookies).Code != 403 {
				t.Fatal("pending consent survived logout")
			}
		})
	}
}

func TestMCPOAuthExpiredCodeAndAbsoluteGrantLifetime(t *testing.T) {
	f := newMCPOAuthFixture(t)
	code := f.code("/mcp")
	now := time.Now()
	f.s.now = func() time.Time { return now.Add(2 * time.Minute) }
	if w, _ := f.token(f.exchangeForm(code, "/mcp")); w.Code != 400 {
		t.Fatal("expired code accepted")
	}
	f.s.now = time.Now
	_, token := f.token(f.exchangeForm(f.code("/mcp"), "/mcp"))
	f.s.now = func() time.Time { return now.Add(23 * time.Hour) }
	refresh := url.Values{"grant_type": {"refresh_token"}, "client_id": {f.client.ID}, "refresh_token": {token["refresh_token"].(string)}}
	w, renewed := f.token(refresh)
	if w.Code != 200 {
		t.Fatalf("refresh before absolute expiry failed: %d %v", w.Code, renewed)
	}
	refresh.Set("refresh_token", renewed["refresh_token"].(string))
	f.s.now = func() time.Time { return now.Add(25 * time.Hour) }
	if w, _ := f.token(refresh); w.Code != 400 {
		t.Fatal("rotation extended grant lifetime")
	}
}

func TestMCPOAuthRegistrationRateLimitAndExpiry(t *testing.T) {
	f := newMCPOAuthFixture(t)
	register := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/auth/mcp/register", strings.NewReader(`{"redirect_uris":["https://client.example/callback"]}`))
		w := httptest.NewRecorder()
		f.s.HandleRegister(w, r)
		return w
	}
	for range 19 {
		if w := register(); w.Code != 201 {
			t.Fatalf("unexpected registration rejection: %d", w.Code)
		}
	}
	if w := register(); w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("missing rate limit: %d", w.Code)
	}
	now := time.Now()
	f.s.now = func() time.Time { return now.Add(11 * time.Minute) }
	if w := register(); w.Code != 201 || len(f.s.clients) != 1 {
		t.Fatalf("unapproved clients not pruned: %d count=%d", w.Code, len(f.s.clients))
	}
}

func TestMCPOAuthConsentEscapesClientMetadata(t *testing.T) {
	f := newMCPOAuthFixture(t)
	client := f.s.clients[f.client.ID]
	client.Name = `<script>alert("untrusted")</script>`
	f.s.clients[client.ID] = client
	page := f.authorize(f.request("/mcp"), f.cookies)
	if strings.Contains(page.Body.String(), "<script>") || !strings.Contains(page.Body.String(), "&lt;script&gt;") || !strings.Contains(page.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("unsafe consent rendering")
	}
}
