package auth

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// cloudMode reports whether Radar is running under Radar Cloud. Mirrored in
// the server package; duplicated here to avoid an import cycle and because
// the auth tightening under cloud-mode is its own concern.
func cloudMode() bool { return os.Getenv("RADAR_CLOUD_MODE") == "true" }

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

			// Try to get user from session cookie first.
			// Cookie-valid path slides the TTL; header-auth path below is a full re-auth.
			if session := ParseSessionCookie(r, cfg.Secret); session != nil {
				// Check if the session has been revoked (backchannel logout)
				if cfg.Revoker != nil && cfg.Revoker.IsRevoked(session.SID) {
					log.Printf("[auth] Revoked session rejected: user=%s sid=%s", session.User.Username, session.SID)
					http.SetCookie(w, ClearSessionCookie())
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					json.NewEncoder(w).Encode(map[string]string{
						"error":    "session revoked",
						"authMode": cfg.Mode,
					})
					return
				}

				// Sliding TTL: re-issue cookie if past half-life or if remaining exceeds
				// the configured TTL (handles TTL downgrade, e.g. 24h → 4h).
				// SetCookie runs before next.ServeHTTP so the handler can't commit headers first.
				remaining := time.Until(session.ExpiresAt)
				if remaining < cfg.CookieTTL/2 || remaining > cfg.CookieTTL {
					sid := session.SID
					if sid == "" {
						// Pre-upgrade cookie without sid — mint one on first sliding re-issue
						sid = NewSessionID()
					}
					http.SetCookie(w, CreateSessionCookie(session.User, sid, session.IDToken, cfg.Secret, cfg.CookieTTL, secure))
					if remaining > cfg.CookieTTL {
						log.Printf("[auth] TTL downgrade detected for user %q: cookie remaining %s exceeds configured TTL %s, snapping",
							session.User.Username, remaining.Round(time.Second), cfg.CookieTTL)
					}
				}
				ctx := ContextWithUser(r.Context(), session.User)
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
					// Header-auth creates a fresh session (new sid each time)
					http.SetCookie(w, CreateSessionCookie(user, NewSessionID(), "", cfg.Secret, cfg.CookieTTL, secure))

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
	// Under cloud-mode the listener is only reachable via the Cloud
	// tunnel, but we still harden against a misconfigured intercept
	// forwarding debug paths or static-asset requests. Keep the exempt
	// set minimal: health for kubelet probes, /auth/* for the login/
	// callback roundtrip. /debug/pprof/* in particular leaks the entire
	// in-memory K8s cache, so it must pass through auth (and is not
	// mounted at all under cloud-mode — see server.go).
	if cloudMode() {
		if path == "/api/health" || strings.HasPrefix(path, "/auth/") {
			return true
		}
		return false
	}

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
