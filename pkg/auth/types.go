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
	CookieTTL time.Duration // default 24h

	// Proxy mode
	UserHeader   string // default "X-Forwarded-User"
	GroupsHeader string // default "X-Forwarded-Groups"

	// OIDC mode
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string
	OIDCGroupsClaim  string // default "groups"
}

// User represents an authenticated user
type User struct {
	Username string   `json:"username"`
	Groups   []string `json:"groups"`
}

// Defaults applies default values to config fields that are empty
func (c *Config) Defaults() {
	if c.CookieTTL == 0 {
		c.CookieTTL = 24 * time.Hour
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
	// Fall back to env var for secret (used by Helm chart)
	if c.Secret == "" {
		c.Secret = os.Getenv("RADAR_AUTH_SECRET")
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
