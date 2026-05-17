package auth

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const oidcStateCookieName = "radar_oidc_state"
const oidcForceLoginCookieName = "radar_force_login"

// OIDCHandler handles the OIDC login flow
type OIDCHandler struct {
	cfg                Config
	provider           *oidc.Provider
	oauth              oauth2.Config
	verifier           *oidc.IDTokenVerifier // used for both ID tokens and logout_tokens (nonce checked manually, not by verifier)
	endSessionEndpoint string                // from OIDC discovery; empty if IdP doesn't support RP-Initiated Logout
	httpClient         *http.Client          // custom TLS client for OIDC provider calls; nil = default
	revoker            *MemoryRevoker        // session revocation store; nil = backchannel logout disabled

	// Discovery: backchannel logout support
	backchannelLogoutSupported        bool
	backchannelLogoutSessionSupported bool
}

// NewOIDCHandler creates a new OIDC handler. Returns an error if the provider
// cannot be discovered (network error, invalid issuer URL, etc.).
func NewOIDCHandler(ctx context.Context, cfg Config) (*OIDCHandler, error) {
	// Build a custom HTTP client for OIDC provider TLS when configured
	var httpClient *http.Client
	if cfg.OIDCCACert != "" {
		caCert, err := os.ReadFile(cfg.OIDCCACert)
		if err != nil {
			return nil, fmt.Errorf("failed to read OIDC CA cert %s: %w", cfg.OIDCCACert, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse OIDC CA cert %s: no valid certificates found", cfg.OIDCCACert)
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.TLSClientConfig = &tls.Config{RootCAs: pool}
		httpClient = &http.Client{Transport: transport}
		log.Printf("[oidc] Using custom CA certificate: %s", cfg.OIDCCACert)
		if cfg.OIDCInsecureSkipVerify {
			log.Printf("[oidc] WARNING: --auth-oidc-insecure-skip-verify is ignored because --auth-oidc-ca-cert is set")
		}
	} else if cfg.OIDCInsecureSkipVerify {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // user-requested via CLI flag
		httpClient = &http.Client{Transport: transport}
		log.Printf("[oidc] WARNING: TLS verification disabled for OIDC provider — do NOT use in production")
	}

	if httpClient != nil {
		ctx = context.WithValue(ctx, oauth2.HTTPClient, httpClient)
	}

	provider, err := oidc.NewProvider(ctx, cfg.OIDCIssuer)
	if err != nil {
		return nil, err
	}

	scopes := cfg.OIDCScopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "profile", "email", "groups"}
	}
	oauthCfg := oauth2.Config{
		ClientID:     cfg.OIDCClientID,
		ClientSecret: cfg.OIDCClientSecret,
		RedirectURL:  cfg.OIDCRedirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       scopes,
	}
	log.Printf("[oidc] Requesting scopes: %v", scopes)

	verifier := provider.Verifier(&oidc.Config{ClientID: cfg.OIDCClientID})

	// Extract endpoints and feature flags from OIDC discovery document
	var providerClaims struct {
		EndSessionEndpoint                string `json:"end_session_endpoint"`
		BackchannelLogoutSupported        bool   `json:"backchannel_logout_supported"`
		BackchannelLogoutSessionSupported bool   `json:"backchannel_logout_session_supported"`
	}
	if err := provider.Claims(&providerClaims); err != nil {
		log.Printf("[oidc] Warning: failed to parse end_session_endpoint from discovery document: %v", err)
	} else if providerClaims.EndSessionEndpoint != "" {
		log.Printf("[oidc] RP-Initiated Logout enabled (end_session_endpoint discovered)")
	} else {
		log.Printf("[oidc] IdP does not advertise end_session_endpoint — will use prompt=login on next auth after logout")
	}

	h := &OIDCHandler{
		cfg:                               cfg,
		provider:                          provider,
		oauth:                             oauthCfg,
		verifier:                          verifier,
		endSessionEndpoint:                providerClaims.EndSessionEndpoint,
		httpClient:                        httpClient,
		backchannelLogoutSupported:        providerClaims.BackchannelLogoutSupported,
		backchannelLogoutSessionSupported: providerClaims.BackchannelLogoutSessionSupported,
	}

	if cfg.OIDCBackchannelLogout {
		switch {
		case providerClaims.BackchannelLogoutSupported && providerClaims.BackchannelLogoutSessionSupported:
			log.Printf("[oidc] Backchannel Logout enabled (sid-based revocation)")
		case providerClaims.BackchannelLogoutSupported:
			log.Printf("[oidc] Backchannel Logout enabled (sub-based revocation — IdP does not advertise sid support)")
		default:
			log.Printf("[oidc] WARNING: --auth-oidc-backchannel-logout is set, but IdP does not advertise backchannel_logout_supported. The endpoint will be registered but this IdP will not use it. Exposure is bounded by cookie TTL (%s).", cfg.CookieTTL)
		}
	}

	return h, nil
}

// HandleLogin redirects to the OIDC provider for authentication
func (h *OIDCHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	// Generate random state nonce and store in a short-lived cookie for CSRF protection
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		log.Printf("[oidc] Failed to generate state nonce: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	state := hex.EncodeToString(b)

	http.SetCookie(w, &http.Cookie{
		Name:     oidcStateCookieName,
		Value:    state,
		Path:     "/",
		MaxAge:   300, // 5 minutes
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteLaxMode,
	})

	// If the user just logged out, force the IdP to show a login prompt instead
	// of silently re-authenticating with an existing SSO session.
	var authOpts []oauth2.AuthCodeOption
	if cookie, err := r.Cookie(oidcForceLoginCookieName); err == nil && cookie.Value == "1" {
		authOpts = append(authOpts, oauth2.SetAuthURLParam("prompt", "login"))
		// Clear the cookie — only force login once
		http.SetCookie(w, &http.Cookie{
			Name:   oidcForceLoginCookieName,
			Path:   "/",
			MaxAge: -1,
		})
	}

	http.Redirect(w, r, h.oauth.AuthCodeURL(state, authOpts...), http.StatusFound)
}

// HandleCallback processes the OIDC callback after authentication
func (h *OIDCHandler) HandleCallback(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// Inject custom TLS client for OIDC provider calls (token exchange, JWKS fetch)
	if h.httpClient != nil {
		ctx = context.WithValue(ctx, oauth2.HTTPClient, h.httpClient)
	}

	// Verify state against cookie to prevent CSRF
	stateCookie, err := r.Cookie(oidcStateCookieName)
	if err != nil || stateCookie.Value == "" {
		http.Error(w, "missing state cookie — please retry login", http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("state") != stateCookie.Value {
		http.Error(w, "invalid state parameter", http.StatusBadRequest)
		return
	}
	// Clear the state cookie
	http.SetCookie(w, &http.Cookie{
		Name:   oidcStateCookieName,
		Path:   "/",
		MaxAge: -1,
	})

	// Exchange code for token
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing authorization code", http.StatusBadRequest)
		return
	}

	token, err := h.oauth.Exchange(ctx, code)
	if err != nil {
		log.Printf("[oidc] Token exchange failed: %v", err)
		http.Error(w, "authentication failed", http.StatusInternalServerError)
		return
	}

	// Extract and verify ID token
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		log.Printf("[oidc] No id_token in token response")
		http.Error(w, "authentication failed: no id_token", http.StatusInternalServerError)
		return
	}

	idToken, err := h.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		log.Printf("[oidc] Token verification failed: %v", err)
		http.Error(w, "authentication failed: invalid token", http.StatusUnauthorized)
		return
	}

	// Extract claims
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		log.Printf("[oidc] Failed to parse claims: %v", err)
		http.Error(w, "authentication failed: invalid claims", http.StatusInternalServerError)
		return
	}

	// Extract username (prefer email, fall back to sub)
	username := ""
	if email, ok := claims["email"].(string); ok && email != "" {
		username = email
	} else if sub, ok := claims["sub"].(string); ok {
		username = sub
	}

	if username == "" {
		log.Printf("[oidc] No username claim (email or sub) in ID token")
		http.Error(w, "authentication failed: no username in token", http.StatusBadRequest)
		return
	}

	// Extract groups from configured claim
	var groups []string
	if groupsClaim, ok := claims[h.cfg.OIDCGroupsClaim]; ok {
		switch g := groupsClaim.(type) {
		case []any:
			for _, v := range g {
				if s, ok := v.(string); ok {
					groups = append(groups, s)
				}
			}
		case string:
			groups = []string{g}
		}
	}

	// Apply OIDC prefix to match Kubernetes API server's --oidc-username-prefix / --oidc-groups-prefix
	if h.cfg.OIDCUsernamePrefix != "" {
		username = h.cfg.OIDCUsernamePrefix + username
	}
	if h.cfg.OIDCGroupsPrefix != "" {
		for i, g := range groups {
			groups[i] = h.cfg.OIDCGroupsPrefix + g
		}
	}

	user := &User{Username: username, Groups: groups}

	// Extract session ID from ID token if present (needed for backchannel logout matching),
	// otherwise generate a random one.
	var sid string
	if s, ok := claims["sid"].(string); ok && s != "" {
		sid = s
		log.Printf("[oidc] Using IdP-provided session ID (sid claim present)")
	} else {
		sid = NewSessionID()
		log.Printf("[oidc] Generated local session ID (IdP did not provide sid claim)")
	}

	// Create session cookie (include raw ID token for RP-Initiated Logout)
	secure := true // OIDC typically behind TLS
	http.SetCookie(w, CreateSessionCookie(user, sid, rawIDToken, h.cfg.Secret, h.cfg.CookieTTL, secure))

	log.Printf("[oidc] User %s authenticated (groups: %v)", username, groups)

	// Redirect to app
	http.Redirect(w, r, "/", http.StatusFound)
}

// HandleLogout clears the local session and, when the IdP supports RP-Initiated
// Logout, returns a JSON response with a "redirectTo" field containing the IdP's
// end_session_endpoint URL so the frontend can redirect the browser to terminate
// the SSO session.
func (h *OIDCHandler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	// Extract ID token before clearing the cookie (needed as id_token_hint)
	var idToken string
	if session := ParseSessionCookie(r, h.cfg.Secret); session != nil {
		idToken = session.IDToken
	}

	http.SetCookie(w, ClearSessionCookie())

	// Set force-login cookie so the next auth request uses prompt=login,
	// preventing silent re-authentication with an existing IdP session.
	// This is especially important for providers like Google that don't
	// support end_session_endpoint.
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{
		Name:     oidcForceLoginCookieName,
		Value:    "1",
		Path:     "/",
		MaxAge:   300, // 5 minutes — enough time for the redirect chain
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})

	resp := map[string]string{"status": "logged out"}

	if h.endSessionEndpoint != "" {
		logoutURL, err := url.Parse(h.endSessionEndpoint)
		if err != nil {
			log.Printf("[oidc] Failed to parse end_session_endpoint %q: %v", h.endSessionEndpoint, err)
		} else {
			q := logoutURL.Query()
			if idToken != "" {
				q.Set("id_token_hint", idToken)
			} else {
				// Fallback for old sessions without stored ID token
				q.Set("client_id", h.cfg.OIDCClientID)
			}
			if h.cfg.OIDCPostLogoutRedirectURL != "" {
				q.Set("post_logout_redirect_uri", h.cfg.OIDCPostLogoutRedirectURL)
			}
			logoutURL.RawQuery = q.Encode()
			resp["redirectTo"] = logoutURL.String()
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// SetRevoker injects the session revocation store for backchannel logout.
// Must be called before the handler is registered if backchannel logout is enabled.
func (h *OIDCHandler) SetRevoker(r *MemoryRevoker) {
	h.revoker = r
}

// backchannelLogoutEventURI is the OIDC event type that must appear in the
// logout_token's "events" claim per the Back-Channel Logout spec §2.4.
const backchannelLogoutEventURI = "http://schemas.openid.net/event/backchannel-logout"

// HandleBackchannelLogout handles POST /auth/backchannel-logout.
// The IdP sends a signed logout_token JWT to notify Radar that a session
// should be revoked. See: https://openid.net/specs/openid-connect-backchannel-1_0.html
func (h *OIDCHandler) HandleBackchannelLogout(w http.ResponseWriter, r *http.Request) {
	// Spec §2.5: response MUST include Cache-Control: no-store
	w.Header().Set("Cache-Control", "no-store")

	if h.revoker == nil {
		http.Error(w, "backchannel logout not configured", http.StatusNotImplemented)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	// Parse logout_token from form body (application/x-www-form-urlencoded)
	if err := r.ParseForm(); err != nil {
		log.Printf("[oidc] Backchannel logout: failed to parse form: %v", err)
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	rawToken := r.FormValue("logout_token")
	if rawToken == "" {
		http.Error(w, "missing logout_token parameter", http.StatusBadRequest)
		return
	}

	// Verify JWT signature, issuer, audience, and expiry using the OIDC provider's JWKS.
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	if h.httpClient != nil {
		ctx = context.WithValue(ctx, oauth2.HTTPClient, h.httpClient)
	}

	idToken, err := h.verifier.Verify(ctx, rawToken)
	if err != nil {
		log.Printf("[oidc] Backchannel logout: token verification failed: %v", err)
		http.Error(w, "invalid logout_token", http.StatusBadRequest)
		return
	}

	// Parse all claims for validation
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		log.Printf("[oidc] Backchannel logout: failed to parse claims: %v", err)
		http.Error(w, "invalid logout_token claims", http.StatusBadRequest)
		return
	}

	// Spec §2.4: MUST contain "events" claim with the backchannel logout event URI
	events, ok := claims["events"].(map[string]any)
	if !ok {
		log.Printf("[oidc] Backchannel logout: missing or invalid 'events' claim")
		http.Error(w, "missing events claim", http.StatusBadRequest)
		return
	}
	if _, hasEvent := events[backchannelLogoutEventURI]; !hasEvent {
		log.Printf("[oidc] Backchannel logout: events claim missing backchannel-logout event URI")
		http.Error(w, "missing backchannel-logout event", http.StatusBadRequest)
		return
	}

	// Spec §2.4: MUST NOT contain a "nonce" claim
	if _, hasNonce := claims["nonce"]; hasNonce {
		log.Printf("[oidc] Backchannel logout: logout_token contains 'nonce' claim (rejected per spec)")
		http.Error(w, "logout_token must not contain nonce", http.StatusBadRequest)
		return
	}

	// Extract sid and/or sub — at least one must be present (spec §2.4)
	sid, _ := claims["sid"].(string)
	sub := idToken.Subject

	if sid == "" && sub == "" {
		log.Printf("[oidc] Backchannel logout: logout_token has neither 'sid' nor 'sub' claim")
		http.Error(w, "logout_token must contain sid or sub", http.StatusBadRequest)
		return
	}

	// JTI dedupe — spec §2.7 requires idempotent handling of retries
	jti, _ := claims["jti"].(string)
	if jti == "" {
		log.Printf("[oidc] Backchannel logout: logout_token has no 'jti' claim — idempotency protection disabled for this token (sub=%s, sid=%s)", sub, sid)
	}
	jtiExpiry := idToken.Expiry
	if jtiExpiry.IsZero() {
		jtiExpiry = time.Now().Add(h.cfg.CookieTTL) // fall back to cookie TTL
	}
	if h.revoker.SeenJTI(jti, jtiExpiry) {
		// Already processed this logout_token — return 200 (idempotent)
		log.Printf("[oidc] Backchannel logout: duplicate jti=%s (idempotent 200)", jti)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Revoke the session
	revocationExpiry := time.Now().Add(h.cfg.CookieTTL)
	if sid != "" {
		h.revoker.Revoke(sid, revocationExpiry)
		log.Printf("[oidc] Backchannel logout: revoked sid=%s (sub=%s, jti=%s)", sid, sub, jti)
	} else {
		// sub-only: we can't do targeted revocation (our store is sid-keyed).
		// Return 501 so the IdP knows this wasn't processed, rather than
		// returning 200 and silently doing nothing.
		log.Printf("[oidc] Backchannel logout: sub-only revocation not supported (sub=%s, jti=%s) — IdP did not provide sid", sub, jti)
		http.Error(w, "sub-only revocation not supported; sid claim required", http.StatusNotImplemented)
		return
	}

	w.WriteHeader(http.StatusOK)
}
