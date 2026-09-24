package mcp

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
)

// SessionTokenEnv enables a per-process bearer on the write-capable /mcp mount.
// Empty: no token. "auto": generate one. Any other value is used as the secret.
const SessionTokenEnv = "RADAR_MCP_SESSION_TOKEN"

// NewSessionToken returns a cryptographically random token for one Radar process.
func NewSessionToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// ResolveSessionToken interprets SessionTokenEnv. Empty yields no token.
func ResolveSessionToken(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if strings.EqualFold(raw, "auto") {
		return NewSessionToken()
	}
	return raw, nil
}

// RequireBearer wraps next so clients must send Authorization: Bearer <token>.
// An empty token returns next unchanged.
func RequireBearer(token string, next http.Handler) http.Handler {
	if token == "" || next == nil {
		return next
	}
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="radar-mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
