package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"time"
)

// Config holds authentication configuration.
// Supports three modes: "none" (default), "proxy" (trust reverse-proxy headers),
// and "oidc" (OpenID Connect login flow).
type Config struct {
	Mode      string        // "none" (default), "proxy", "oidc"
	Secret    string        // HMAC signing key for session cookies
	CookieTTL time.Duration // default 4h, sliding

	// Proxy mode
	UserHeader     string // default "X-Forwarded-User"
	GroupsHeader   string // default "X-Forwarded-Groups"
	ProxyLogoutURL string // optional, URL the logout button redirects to so the upstream proxy session is torn down (e.g. oauth2-proxy /oauth2/sign_out)

	// Session revocation (optional, used by backchannel logout)
	Revoker SessionRevoker

	// OIDC mode
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL           string
	OIDCGroupsClaim           string   // default "groups"
	OIDCScopes                []string // OAuth2 scopes requested at authorization; default ["openid", "profile", "email", "groups"]
	OIDCPostLogoutRedirectURL  string // optional, URL to redirect after IdP logout
	OIDCUsernamePrefix         string // prefix added to OIDC username for K8s impersonation (e.g., "oidc:")
	OIDCGroupsPrefix           string // prefix added to OIDC groups for K8s impersonation (e.g., "oidc:")
	OIDCInsecureSkipVerify     bool   // skip TLS verification for OIDC provider (dev/test only)
	OIDCCACert                 string // path to CA certificate file for OIDC provider TLS
	OIDCBackchannelLogout      bool   // enable backchannel logout endpoint
}

// SessionRevoker checks whether a session has been revoked (e.g., via OIDC
// backchannel logout). Used by the auth middleware to reject revoked sessions.
type SessionRevoker interface {
	IsRevoked(sid string) bool
}

// User represents an authenticated user
type User struct {
	Username string   `json:"username"`
	Groups   []string `json:"groups"`
}

// Defaults applies default values to config fields that are empty
func (c *Config) Defaults() {
	if c.CookieTTL == 0 {
		c.CookieTTL = 4 * time.Hour // default 4h, sliding — extends on activity
	}
	if c.UserHeader == "" {
		c.UserHeader = "X-Forwarded-User"
	}
	if c.GroupsHeader == "" {
		c.GroupsHeader = "X-Forwarded-Groups"
	}
	if c.OIDCGroupsClaim == "" {
		c.OIDCGroupsClaim = "groups"
	}
	if len(c.OIDCScopes) == 0 {
		c.OIDCScopes = []string{"openid", "profile", "email", "groups"}
	}
	// Fall back to env vars for secrets (used by Helm chart)
	if c.Secret == "" {
		c.Secret = os.Getenv("RADAR_AUTH_SECRET")
	}
	if c.OIDCClientSecret == "" {
		c.OIDCClientSecret = os.Getenv("RADAR_OIDC_CLIENT_SECRET")
	}
	// Auto-generate secret if still empty and auth is enabled
	if c.Secret == "" && c.Enabled() {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			log.Fatalf("[auth] Failed to generate session secret: %v", err)
		}
		c.Secret = hex.EncodeToString(b)
		log.Printf("[auth] Auto-generated session secret (sessions will not survive restarts)")
	}
}

// Enabled returns true if auth mode is not "none"
func (c *Config) Enabled() bool {
	return c.Mode != "" && c.Mode != "none"
}

// contextKey is the context key for the authenticated user
type contextKey struct{}

// UserFromContext retrieves the authenticated user from the request context.
// Returns nil when auth is disabled or the user is not authenticated.
func UserFromContext(ctx context.Context) *User {
	user, _ := ctx.Value(contextKey{}).(*User)
	return user
}

// ContextWithUser returns a new context with the authenticated user set.
func ContextWithUser(ctx context.Context, user *User) context.Context {
	return context.WithValue(ctx, contextKey{}, user)
}
