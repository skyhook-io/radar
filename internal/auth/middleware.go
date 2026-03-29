package auth

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

// Authenticate returns a chi middleware that extracts user identity from
// proxy headers or session cookies. Returns 401 if unauthenticated.
// Exempt paths (health, auth endpoints) are passed through.
// Soft-auth paths (e.g. /api/auth/me) attempt auth but don't 401 on failure.
func Authenticate(cfg Config) func(http.Handler) http.Handler {
	cfg.Defaults()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Exempt paths that don't require auth
			if isExemptPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			// Determine whether to set Secure flag on cookies per-request.
			// OIDC is always behind TLS. Proxy mode detects TLS via X-Forwarded-Proto
			// (set by the upstream reverse proxy) or a direct TLS connection.
			secure := cfg.Mode == "oidc" || r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"

			// Try to get user from session cookie first
			if user := ParseSessionCookie(r, cfg.Secret); user != nil {
				ctx := ContextWithUser(r.Context(), user)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// In proxy mode, extract from headers and create session
			if cfg.Mode == "proxy" {
				username := r.Header.Get(cfg.UserHeader)
				if username != "" {
					var groups []string
					if g := r.Header.Get(cfg.GroupsHeader); g != "" {
						for _, part := range strings.Split(g, ",") {
							if trimmed := strings.TrimSpace(part); trimmed != "" {
								groups = append(groups, trimmed)
							}
						}
					}

					user := &User{Username: username, Groups: groups}

					// Set session cookie so subsequent requests don't need headers
					http.SetCookie(w, CreateSessionCookie(user, cfg.Secret, cfg.CookieTTL, secure))

					ctx := ContextWithUser(r.Context(), user)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}

			// Soft-auth paths: pass through without user (handler decides response)
			if isSoftAuthPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			// No valid auth found
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{
				"error":    "authentication required",
				"authMode": cfg.Mode,
			})
		})
	}
}

// isExemptPath returns true for paths that don't require authentication
func isExemptPath(path string) bool {
	exemptPrefixes := []string{
		"/api/health",
		"/api/connection",
		"/auth/",
	}
	for _, prefix := range exemptPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	// Static assets don't require auth
	if !strings.HasPrefix(path, "/api/") && !strings.HasPrefix(path, "/mcp") {
		return true
	}
	return false
}

// isSoftAuthPath returns true for paths that should attempt auth but not
// require it. These endpoints work with or without a user in context.
func isSoftAuthPath(path string) bool {
	return path == "/api/auth/me"
}

// AuditLog logs a write operation with user identity
func AuditLog(r *http.Request, namespace, name string) {
	user := UserFromContext(r.Context())
	if user == nil {
		return
	}
	log.Printf("[audit] user=%s groups=%v %s %s ns=%s name=%s",
		user.Username, user.Groups, r.Method, r.URL.Path, namespace, name)
}
