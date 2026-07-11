package auth

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/cloud"
)

// cloudMode reports whether Radar is running under Radar Cloud. Reads
// the resolved deployment mode from internal/cloud (single source of
// truth across server, auth, and main; normalizes RADAR_CLOUD_MODE
// via strconv.ParseBool so a typo'd "True" / "1" doesn't silently
// degrade to OSS mode).
func cloudMode() bool { return cloud.Mode() }

// Authenticate returns a chi middleware that extracts user identity from
// proxy headers or session cookies. Returns 401 if unauthenticated.
// Mode-specific exempt paths are passed through.
// Outside Cloud, soft-auth paths attempt auth but don't 401 on failure.
func Authenticate(cfg Config) func(http.Handler) http.Handler {
	cfg.Defaults()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Exempt paths that don't require auth
			if isExemptPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			cloudProxyMode := cfg.Mode == "proxy" && cloudMode()
			if cloudProxyMode && !cloud.IsAuthenticatedTunnelRequest(r.Context()) {
				// Forwarded identity is authoritative only inside the yamux transport
				// established with the cluster token. Radar also has an ordinary TCP
				// listener for kubelet health checks; a pod reaching that listener can
				// spoof headers, but it cannot forge the private context marker.
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]string{
					"error":    "authentication required",
					"authMode": cfg.Mode,
				})
				return
			}

			// Determine whether to set Secure flag on cookies per-request.
			// OIDC is always behind TLS. Proxy mode detects TLS via X-Forwarded-Proto
			// (set by the upstream reverse proxy) or a direct TLS connection.
			secure := cfg.Mode == "oidc" || r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"

			// Cloud proxy identity is authoritative on every tunneled request. A
			// browser-carried Radar session must never override the Hub-injected
			// user/groups if an upstream cookie-strip defense regresses.
			var session *Session
			if !cloudProxyMode {
				session = ParseSessionCookie(r, cfg.Secret)
			}
			if session != nil {
				// Check if the session has been revoked (backchannel logout)
				if cfg.Revoker != nil && cfg.Revoker.IsRevoked(session.SID) {
					log.Printf("[auth] Revoked session rejected: user=%s sid=%s", session.User.Username, session.SID)
					for _, c := range ClearSessionCookie(r) {
						http.SetCookie(w, c)
					}
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
					cookies := CreateSessionCookie(session.User, sid, session.IDToken, cfg.Secret, cfg.CookieTTL, secure)
					for _, c := range cookies {
						http.SetCookie(w, c)
					}
					if remaining > cfg.CookieTTL {
						log.Printf("[auth] TTL downgrade detected for user %q: cookie remaining %s exceeds configured TTL %s, snapping",
							session.User.Username, remaining.Round(time.Second), cfg.CookieTTL)
					}
				}
				ctx := ContextWithUser(r.Context(), session.User)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// In proxy mode, extract identity from headers. Standalone proxy mode
			// caches it in a session cookie; Cloud proxy mode stays header-only.
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
					if !cloudProxyMode {
						cookies := CreateSessionCookie(user, NewSessionID(), "", cfg.Secret, cfg.CookieTTL, secure)
						for _, c := range cookies {
							http.SetCookie(w, c)
						}
					}
					ctx := ContextWithUser(r.Context(), user)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}

			// Soft-auth paths pass through without a user outside Cloud. Cloud proxy
			// mode requires the Hub identity headers on every non-exempt request.
			if !cloudProxyMode && isSoftAuthPath(r.URL.Path) {
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
	// Under cloud-mode, the full handler is reachable only via the authenticated
	// tunnel and the ordinary TCP listener is health-only. Keep this auth-layer
	// check as defense in depth: /debug/pprof/* in particular leaks the entire
	// in-memory K8s cache, so it must never bypass the Hub identity boundary (and
	// is not mounted at all under cloud-mode — see server.go).
	if cloudMode() {
		// Cloud owns authentication; Radar's own login/callback endpoints must
		// not be reachable without the Hub identity boundary. The exact health
		// path remains public for kubelet probes on the health-only TCP listener.
		return path == "/api/health"
	}

	// /api/connection is deliberately NOT exempt: POST /api/connection/retry
	// is state-changing (kills all exec/port-forward sessions, reinitializes
	// the informer cache) and GET /api/connection leaks kubeconfig context
	// names. The terminal re-auth flow chains a retry curl only on no-auth
	// installs, where this middleware isn't mounted at all.
	exemptPrefixes := []string{
		"/api/health",
		"/auth/",
	}
	for _, prefix := range exemptPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	// Static assets don't require auth. /debug/* (pprof) is excluded: it's
	// mounted on every non-cloud build, and the fallthrough would otherwise
	// expose it unauthenticated whenever auth is enabled — /debug/pprof/heap
	// leaks the entire in-memory K8s cache (every Secret, ConfigMap, Pod
	// spec). /metrics stays open by this fallthrough: operational counters
	// only, scraped by Prometheus.
	if !strings.HasPrefix(path, "/api/") && !strings.HasPrefix(path, "/mcp") && !strings.HasPrefix(path, "/debug/") {
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
	// %q escapes any control characters (e.g. CR/LF) so a crafted path or name
	// can't forge or split audit log lines.
	log.Printf("[audit] user=%q groups=%q %s path=%q ns=%q name=%q",
		user.Username, user.Groups, r.Method, r.URL.Path, namespace, name)
}
