package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// DefaultCookieName is the default session cookie name
const DefaultCookieName = "radar_session"

// cookiePayload is the data stored in the session cookie
type cookiePayload struct {
	Username  string   `json:"u"`
	Groups    []string `json:"g,omitempty"`
	ExpiresAt int64    `json:"e"`
}

// CreateSessionCookie creates a signed session cookie for the given user.
// Format: base64(json) + "." + base64(hmac-sha256)
func CreateSessionCookie(user *User, secret string, ttl time.Duration, secure bool) *http.Cookie {
	payload := cookiePayload{
		Username:  user.Username,
		Groups:    user.Groups,
		ExpiresAt: time.Now().Add(ttl).Unix(),
	}

	data, err := json.Marshal(payload)
	if err != nil {
		// Should never happen for this struct — fail loudly rather than issue a broken cookie
		log.Fatalf("[auth] Failed to marshal session cookie payload for user %s: %v", user.Username, err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(data)

	sig := signData(encoded, secret)

	return &http.Cookie{
		Name:     DefaultCookieName,
		Value:    encoded + "." + sig,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl.Seconds()),
	}
}

// ParseSessionCookie validates and parses a session cookie.
// Returns nil if the cookie is missing, invalid, or expired.
func ParseSessionCookie(r *http.Request, secret string) *User {
	cookie, err := r.Cookie(DefaultCookieName)
	if err != nil {
		return nil
	}

	parts := strings.SplitN(cookie.Value, ".", 2)
	if len(parts) != 2 {
		return nil
	}

	encoded, sig := parts[0], parts[1]

	// Verify HMAC signature
	expected := signData(encoded, secret)
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		log.Printf("[auth] Session cookie HMAC verification failed — possible tampered cookie from %s", r.RemoteAddr)
		return nil
	}

	// Decode payload
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil
	}

	var p cookiePayload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil
	}

	// Check expiration
	if time.Now().Unix() > p.ExpiresAt {
		log.Printf("[auth] Session cookie expired for user %q — prompting re-auth", p.Username)
		return nil
	}

	return &User{
		Username: p.Username,
		Groups:   p.Groups,
	}
}

// ClearSessionCookie returns a cookie that clears the session
func ClearSessionCookie() *http.Cookie {
	return &http.Cookie{
		Name:     DefaultCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	}
}

// signData computes HMAC-SHA256 of the given data with the secret
func signData(data, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprint(mac, data)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
