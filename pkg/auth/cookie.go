package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultCookieName is the default session cookie name
const DefaultCookieName = "radar_session"

// maxCookieSize is the safe limit for a single cookie value. RFC 6265 requires
// browsers to support at least 4096 bytes per cookie, but some proxies and CDNs
// enforce stricter limits. We use 3800 to leave headroom for the cookie name,
// attributes (Path, Secure, HttpOnly, SameSite, MaxAge), and the chunk suffix.
const maxCookieSize = 3800

// cookieChunkSuffix names the per-chunk cookies (radar_session_chunk_0, _1, …).
const cookieChunkSuffix = "_chunk_"

// cookieChunkCountSuffix names the meta-cookie holding the chunk count.
const cookieChunkCountSuffix = "_chunks"

// maxCookieChunks bounds chunk reassembly so a forged _chunks meta-cookie can't
// drive an unbounded read loop. 16 chunks ≈ 59 KB of reassembled value, well
// beyond any realistic OIDC token + group set.
const maxCookieChunks = 16

// Session represents a parsed session cookie.
type Session struct {
	User      *User
	SID       string    // stable session identifier (empty for pre-upgrade cookies)
	IDToken   string    // raw OIDC id_token for RP-Initiated Logout
	ExpiresAt time.Time // when the cookie expires
}

// cookiePayload is the data stored in the session cookie
type cookiePayload struct {
	Username  string   `json:"u"`
	Groups    []string `json:"g,omitempty"`
	ExpiresAt int64    `json:"e"`
	IDToken   string   `json:"t,omitempty"` // raw OIDC id_token for RP-Initiated Logout
	SID       string   `json:"s,omitempty"` // session ID for backchannel logout revocation
}

// NewSessionID generates a random 16-byte hex session ID.
func NewSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("[auth] Failed to generate session ID: %v", err))
	}
	return hex.EncodeToString(b)
}

// CreateSessionCookie builds the signed session cookie(s) for the given user.
// Format: base64(json) + "." + base64(hmac-sha256). The sid must be non-empty —
// use NewSessionID() to generate one.
//
// Most sessions fit in a single cookie. When the signed value exceeds
// maxCookieSize (many groups + a large OIDC ID token), the ID token is dropped
// first — it's only needed for RP-Initiated Logout's id_token_hint and falls
// back to client_id gracefully. If still too large, the value is split across
// numbered chunk cookies plus a meta-cookie holding the count, so IdPs that
// emit oversized tokens (e.g. Keycloak with many claims) don't get silently
// rejected by the browser's ~4 KB per-cookie limit. Callers must SetCookie each
// returned cookie.
func CreateSessionCookie(user *User, sid, idToken, secret string, ttl time.Duration, secure bool) []*http.Cookie {
	if sid == "" {
		panic(fmt.Sprintf("[auth] CreateSessionCookie called with empty sid for user %s", user.Username))
	}

	payload := cookiePayload{
		Username:  user.Username,
		Groups:    user.Groups,
		ExpiresAt: time.Now().Add(ttl).Unix(),
		IDToken:   idToken,
		SID:       sid,
	}

	value := buildCookieValue(payload, secret)

	if len(value) > maxCookieSize && payload.IDToken != "" {
		log.Printf("[auth] Session cookie exceeds %d bytes (%d), dropping ID token to fit",
			maxCookieSize, len(value))
		payload.IDToken = ""
		value = buildCookieValue(payload, secret)
	}

	if len(value) <= maxCookieSize {
		// Single cookie. Clear the chunk meta-cookie so a previous chunked
		// representation (if any) can't be reassembled: parse only follows
		// the chunk path when the meta-cookie is present, so dropping it
		// neutralizes any stale chunks (they expire on their own TTL).
		return []*http.Cookie{
			newSessionCookie(DefaultCookieName, value, ttl, secure),
			expireCookie(DefaultCookieName+cookieChunkCountSuffix, secure),
		}
	}

	log.Printf("[auth] Session cookie is %d bytes (limit %d) — splitting into chunked cookies",
		len(value), maxCookieSize)
	return createChunkedCookies(DefaultCookieName, value, ttl, secure)
}

// newSessionCookie builds a single session cookie with the standard attributes.
func newSessionCookie(name, value string, ttl time.Duration, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl.Seconds()),
	}
}

// createChunkedCookies splits an oversized signed value across numbered chunk
// cookies plus a meta-cookie holding the count. Reserve headroom under
// maxCookieSize for the (longer) chunk cookie names + attributes.
func createChunkedCookies(name, value string, ttl time.Duration, secure bool) []*http.Cookie {
	chunks := splitString(value, maxCookieSize-100)
	if len(chunks) > maxCookieChunks {
		// Parse rejects counts above maxCookieChunks, so a session this large
		// can't round-trip. Don't issue chunks the server will then refuse
		// (that would loop login → 401); clear any prior representation and
		// surface the misconfiguration instead.
		log.Printf("[auth] ERROR: session value needs %d chunks (max %d) — cannot issue a usable session; reduce the number of OIDC groups/claims",
			len(chunks), maxCookieChunks)
		return []*http.Cookie{
			expireCookie(name, secure),
			expireCookie(name+cookieChunkCountSuffix, secure),
		}
	}
	cookies := make([]*http.Cookie, 0, len(chunks)+2)

	// Clear any stale single main cookie so the chunked session is authoritative
	// (parse prefers a non-empty main cookie over chunks).
	cookies = append(cookies, expireCookie(name, secure))
	for i, chunk := range chunks {
		cookies = append(cookies, newSessionCookie(fmt.Sprintf("%s%s%d", name, cookieChunkSuffix, i), chunk, ttl, secure))
	}
	cookies = append(cookies, newSessionCookie(name+cookieChunkCountSuffix, strconv.Itoa(len(chunks)), ttl, secure))
	return cookies
}

func splitString(s string, chunkSize int) []string {
	if len(s) <= chunkSize {
		return []string{s}
	}
	var chunks []string
	for i := 0; i < len(s); i += chunkSize {
		end := min(i+chunkSize, len(s))
		chunks = append(chunks, s[i:end])
	}
	return chunks
}

// ParseSessionCookie validates and parses a session cookie.
// Returns nil if the cookie is missing, invalid, or expired.
// Pre-upgrade cookies without a SID parse successfully with Session.SID == "".
//
// The single main cookie is preferred; chunk reassembly is the fallback. A
// session that shrinks back to one cookie therefore parses correctly even if
// stale chunk cookies from a previous larger session are still in the browser
// (those expire with the TTL and are cleared on logout).
func ParseSessionCookie(r *http.Request, secret string) *Session {
	if cookie, err := r.Cookie(DefaultCookieName); err == nil && cookie.Value != "" {
		return parseCookieValue(cookie.Value, secret, r.RemoteAddr)
	}

	chunksCookie, err := r.Cookie(DefaultCookieName + cookieChunkCountSuffix)
	if err != nil {
		return nil
	}
	numChunks, err := strconv.Atoi(chunksCookie.Value)
	if err != nil || numChunks <= 0 || numChunks > maxCookieChunks {
		return nil
	}

	var fullValue strings.Builder
	for i := range numChunks {
		chunk, err := r.Cookie(fmt.Sprintf("%s%s%d", DefaultCookieName, cookieChunkSuffix, i))
		if err != nil {
			return nil
		}
		fullValue.WriteString(chunk.Value)
	}
	return parseCookieValue(fullValue.String(), secret, r.RemoteAddr)
}

// parseCookieValue verifies the HMAC over the (reassembled) value and decodes
// the payload. Because the signature covers the whole value, a chunk that is
// dropped, reordered, or forged yields a different reassembly and fails here.
func parseCookieValue(cookieValue, secret, remoteAddr string) *Session {
	parts := strings.SplitN(cookieValue, ".", 2)
	if len(parts) != 2 {
		return nil
	}

	encoded, sig := parts[0], parts[1]

	// Verify HMAC signature
	expected := signData(encoded, secret)
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		log.Printf("[auth] Session cookie HMAC verification failed — possible tampered cookie from %q", remoteAddr)
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

	return &Session{
		User: &User{
			Username: p.Username,
			Groups:   p.Groups,
		},
		SID:       p.SID,
		IDToken:   p.IDToken,
		ExpiresAt: time.Unix(p.ExpiresAt, 0),
	}
}

// buildCookieValue marshals the payload and signs it: base64(json) + "." + base64(hmac).
func buildCookieValue(p cookiePayload, secret string) string {
	data, err := json.Marshal(p)
	if err != nil {
		log.Fatalf("[auth] Failed to marshal session cookie payload for user %q: %v", p.Username, err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(data)
	return encoded + "." + signData(encoded, secret)
}

// ClearSessionCookie returns the cookie(s) that clear the session. It always
// clears the main cookie; when the request carries the chunk meta-cookie it
// also clears each chunk and the meta-cookie, so a chunked session can't
// survive logout by being reassembled on the next request.
func ClearSessionCookie(r *http.Request) []*http.Cookie {
	// Match the Secure attribute of the session cookies being cleared: a
	// non-Secure deletion cookie may not reliably evict a Secure one in every
	// browser. OIDC issues Secure cookies, so over HTTPS we must clear Secure.
	secure := r != nil && (r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https")
	cookies := []*http.Cookie{expireCookie(DefaultCookieName, secure)}

	if r == nil {
		return cookies
	}
	// If the session was chunked, clear the meta-cookie and every possible
	// chunk index — not just the advertised count — so no chunk residue
	// survives logout even if the count is stale (after a shrink) or corrupted.
	if _, err := r.Cookie(DefaultCookieName + cookieChunkCountSuffix); err == nil {
		cookies = append(cookies, expireCookie(DefaultCookieName+cookieChunkCountSuffix, secure))
		for i := range maxCookieChunks {
			cookies = append(cookies, expireCookie(fmt.Sprintf("%s%s%d", DefaultCookieName, cookieChunkSuffix, i), secure))
		}
	}
	return cookies
}

// expireCookie returns a cookie that immediately clears the named cookie. It
// mirrors the Secure attribute of the cookie it replaces so the deletion is
// honored regardless of the original's Secure flag.
func expireCookie(name string, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		MaxAge:   -1,
	}
}

// signData computes HMAC-SHA256 of the given data with the secret
func signData(data, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprint(mac, data)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
