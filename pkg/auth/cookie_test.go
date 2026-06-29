package auth

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// singleCookie asserts that CreateSessionCookie returned exactly one cookie
// (the common, non-chunked case) and returns it.
func singleCookie(t *testing.T, cookies []*http.Cookie) *http.Cookie {
	t.Helper()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	return cookies[0]
}

// addCookies attaches every cookie to the request, mimicking a browser sending
// back all cookies the server previously set.
func addCookies(req *http.Request, cookies []*http.Cookie) {
	for _, c := range cookies {
		req.AddCookie(c)
	}
}

func TestCreateAndParseSessionCookie(t *testing.T) {
	secret := "test-secret-key"
	user := &User{Username: "alice", Groups: []string{"devs", "admins"}}
	sid := NewSessionID()
	ttl := 1 * time.Hour

	cookie := singleCookie(t, CreateSessionCookie(user, sid, "", secret, ttl, false))

	// Verify cookie properties
	if cookie.Name != DefaultCookieName {
		t.Errorf("cookie name = %q, want %q", cookie.Name, DefaultCookieName)
	}
	if !cookie.HttpOnly {
		t.Error("cookie should be HttpOnly")
	}
	if cookie.Secure {
		t.Error("cookie should not be Secure when secure=false")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie SameSite = %v, want Lax", cookie.SameSite)
	}
	if cookie.MaxAge != 3600 {
		t.Errorf("cookie MaxAge = %d, want 3600", cookie.MaxAge)
	}

	// Parse it back
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookie)

	parsed := ParseSessionCookie(req, secret)
	if parsed == nil {
		t.Fatal("ParseSessionCookie returned nil for valid cookie")
	}
	if parsed.User.Username != "alice" {
		t.Errorf("username = %q, want %q", parsed.User.Username, "alice")
	}
	if len(parsed.User.Groups) != 2 || parsed.User.Groups[0] != "devs" || parsed.User.Groups[1] != "admins" {
		t.Errorf("groups = %v, want [devs admins]", parsed.User.Groups)
	}
	if parsed.SID != sid {
		t.Errorf("SID = %q, want %q", parsed.SID, sid)
	}
}

func TestParseSessionCookie_WrongSecret(t *testing.T) {
	user := &User{Username: "alice"}
	cookie := singleCookie(t, CreateSessionCookie(user, NewSessionID(), "", "secret-1", 1*time.Hour, false))

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookie)

	parsed := ParseSessionCookie(req, "secret-2")
	if parsed != nil {
		t.Error("ParseSessionCookie should return nil for wrong secret")
	}
}

func TestParseSessionCookie_Expired(t *testing.T) {
	user := &User{Username: "alice"}
	// TTL of -1 second = already expired
	cookie := singleCookie(t, CreateSessionCookie(user, NewSessionID(), "", "secret", -1*time.Second, false))

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookie)

	parsed := ParseSessionCookie(req, "secret")
	if parsed != nil {
		t.Error("ParseSessionCookie should return nil for expired cookie")
	}
}

func TestParseSessionCookie_NoCookie(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	parsed := ParseSessionCookie(req, "secret")
	if parsed != nil {
		t.Error("ParseSessionCookie should return nil when no cookie present")
	}
}

func TestParseSessionCookie_TamperedPayload(t *testing.T) {
	user := &User{Username: "alice"}
	cookie := singleCookie(t, CreateSessionCookie(user, NewSessionID(), "", "secret", 1*time.Hour, false))

	// Tamper with the payload (change first char)
	val := cookie.Value
	if val[0] == 'a' {
		cookie.Value = "b" + val[1:]
	} else {
		cookie.Value = "a" + val[1:]
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookie)

	parsed := ParseSessionCookie(req, "secret")
	if parsed != nil {
		t.Error("ParseSessionCookie should return nil for tampered cookie")
	}
}

func TestParseSessionCookie_MalformedValue(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: DefaultCookieName, Value: "not-a-valid-cookie"})

	parsed := ParseSessionCookie(req, "secret")
	if parsed != nil {
		t.Error("ParseSessionCookie should return nil for malformed cookie (no dot)")
	}
}

func TestCreateSessionCookie_Secure(t *testing.T) {
	user := &User{Username: "alice"}
	cookie := singleCookie(t, CreateSessionCookie(user, NewSessionID(), "", "secret", 1*time.Hour, true))
	if !cookie.Secure {
		t.Error("cookie should be Secure when secure=true")
	}
}

func TestCreateSessionCookie_NoGroups(t *testing.T) {
	user := &User{Username: "bob"}
	cookie := singleCookie(t, CreateSessionCookie(user, NewSessionID(), "", "secret", 1*time.Hour, false))

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookie)

	parsed := ParseSessionCookie(req, "secret")
	if parsed == nil {
		t.Fatal("ParseSessionCookie returned nil")
	}
	if parsed.User.Username != "bob" {
		t.Errorf("username = %q, want %q", parsed.User.Username, "bob")
	}
	if len(parsed.User.Groups) != 0 {
		t.Errorf("groups = %v, want empty", parsed.User.Groups)
	}
}

func TestClearSessionCookie(t *testing.T) {
	cookie := singleCookie(t, ClearSessionCookie(nil))
	if cookie.Name != DefaultCookieName {
		t.Errorf("cookie name = %q, want %q", cookie.Name, DefaultCookieName)
	}
	if cookie.MaxAge != -1 {
		t.Errorf("cookie MaxAge = %d, want -1", cookie.MaxAge)
	}
}

func TestSignData_Deterministic(t *testing.T) {
	sig1 := signData("hello", "secret")
	sig2 := signData("hello", "secret")
	if sig1 != sig2 {
		t.Error("signData should be deterministic")
	}
}

func TestSignData_DifferentInputs(t *testing.T) {
	sig1 := signData("hello", "secret")
	sig2 := signData("world", "secret")
	if sig1 == sig2 {
		t.Error("signData should produce different signatures for different inputs")
	}
}

func TestSignData_DifferentSecrets(t *testing.T) {
	sig1 := signData("hello", "secret1")
	sig2 := signData("hello", "secret2")
	if sig1 == sig2 {
		t.Error("signData should produce different signatures for different secrets")
	}
}

func TestCreateSessionCookie_WithIDToken(t *testing.T) {
	secret := "test-secret"
	user := &User{Username: "alice", Groups: []string{"devs"}}
	sid := NewSessionID()
	idToken := "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.test-payload.test-sig"

	cookie := singleCookie(t, CreateSessionCookie(user, sid, idToken, secret, 1*time.Hour, false))

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookie)

	parsed := ParseSessionCookie(req, secret)
	if parsed == nil {
		t.Fatal("ParseSessionCookie returned nil for cookie with ID token")
	}
	if parsed.User.Username != "alice" {
		t.Errorf("username = %q, want %q", parsed.User.Username, "alice")
	}
	if parsed.IDToken != idToken {
		t.Errorf("IDToken = %q, want %q", parsed.IDToken, idToken)
	}
	if parsed.SID != sid {
		t.Errorf("SID = %q, want %q", parsed.SID, sid)
	}
}

func TestSessionIDToken_NoIDToken(t *testing.T) {
	secret := "test-secret"
	user := &User{Username: "alice"}

	cookie := singleCookie(t, CreateSessionCookie(user, NewSessionID(), "", secret, 1*time.Hour, false))
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookie)

	parsed := ParseSessionCookie(req, secret)
	if parsed == nil {
		t.Fatal("ParseSessionCookie returned nil")
	}
	if parsed.IDToken != "" {
		t.Errorf("IDToken = %q, want empty string", parsed.IDToken)
	}
}

func TestCreateSessionCookie_WithSID(t *testing.T) {
	secret := "test-secret"
	user := &User{Username: "alice"}
	sid := "abcdef0123456789abcdef0123456789"

	cookie := singleCookie(t, CreateSessionCookie(user, sid, "", secret, 1*time.Hour, false))

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookie)

	parsed := ParseSessionCookie(req, secret)
	if parsed == nil {
		t.Fatal("ParseSessionCookie returned nil")
	}
	if parsed.SID != sid {
		t.Errorf("SID = %q, want %q", parsed.SID, sid)
	}
}

func TestParseSessionCookie_LegacyCookieWithoutSID(t *testing.T) {
	// Simulate a pre-upgrade cookie that doesn't have the "s" field
	secret := "test-secret"
	payload := struct {
		Username  string   `json:"u"`
		Groups    []string `json:"g,omitempty"`
		ExpiresAt int64    `json:"e"`
		IDToken   string   `json:"t,omitempty"`
	}{
		Username:  "alice",
		Groups:    []string{"devs"},
		ExpiresAt: time.Now().Add(1 * time.Hour).Unix(),
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(data)
	sig := signData(encoded, secret)

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{
		Name:  DefaultCookieName,
		Value: encoded + "." + sig,
	})

	parsed := ParseSessionCookie(req, secret)
	if parsed == nil {
		t.Fatal("ParseSessionCookie should handle legacy cookies without sid")
	}
	if parsed.User.Username != "alice" {
		t.Errorf("username = %q, want %q", parsed.User.Username, "alice")
	}
	if parsed.SID != "" {
		t.Errorf("SID = %q, want empty string for legacy cookie", parsed.SID)
	}
}

func TestNewSessionID_Unique(t *testing.T) {
	id1 := NewSessionID()
	id2 := NewSessionID()

	if id1 == id2 {
		t.Error("NewSessionID should produce unique values")
	}
	if len(id1) != 32 {
		t.Errorf("NewSessionID length = %d, want 32 (16 bytes hex)", len(id1))
	}
	if len(id2) != 32 {
		t.Errorf("NewSessionID length = %d, want 32 (16 bytes hex)", len(id2))
	}
}

func TestParseSessionCookie_ExpiresAt(t *testing.T) {
	secret := "test-secret"
	user := &User{Username: "alice"}
	ttl := 2 * time.Hour

	cookie := singleCookie(t, CreateSessionCookie(user, NewSessionID(), "", secret, ttl, false))

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookie)

	parsed := ParseSessionCookie(req, secret)
	if parsed == nil {
		t.Fatal("ParseSessionCookie returned nil")
	}

	// ExpiresAt should be approximately now + ttl (within a few seconds)
	expected := time.Now().Add(ttl)
	diff := parsed.ExpiresAt.Sub(expected)
	if diff < -5*time.Second || diff > 5*time.Second {
		t.Errorf("ExpiresAt off by %v, want within 5s of now+2h", diff)
	}
}

func TestCreateSessionCookie_DropsIDTokenWhenTooLarge(t *testing.T) {
	secret := "test-secret"
	// Build a cookie that's over maxCookieSize bytes with the ID token, but under without it.
	groups := make([]string, 40)
	for i := range groups {
		groups[i] = "org:engineering:team-" + strings.Repeat("x", 10)
	}
	user := &User{Username: "alice@example.com", Groups: groups}
	sid := NewSessionID()
	largeIDToken := strings.Repeat("x", 2000)

	// First verify the cookie WITHOUT ID token fits in a single cookie
	smallCookie := singleCookie(t, CreateSessionCookie(user, sid, "", secret, 1*time.Hour, false))
	if len(smallCookie.Value) > maxCookieSize {
		t.Skipf("groups alone exceed %d bytes (%d) — can't test ID token drop", maxCookieSize, len(smallCookie.Value))
	}

	// Now create with the large ID token — should trigger the drop and still fit one cookie
	cookie := singleCookie(t, CreateSessionCookie(user, sid, largeIDToken, secret, 1*time.Hour, false))

	// Parse and verify the cookie is still valid
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookie)
	parsed := ParseSessionCookie(req, secret)
	if parsed == nil {
		t.Fatal("ParseSessionCookie returned nil for size-capped cookie")
	}
	if parsed.User.Username != "alice@example.com" {
		t.Errorf("username = %q, want alice@example.com", parsed.User.Username)
	}
	if parsed.SID != sid {
		t.Errorf("SID lost after size cap")
	}
	if parsed.IDToken == largeIDToken {
		t.Error("ID token should have been dropped to fit cookie size limit")
	}
	if len(cookie.Value) > maxCookieSize {
		t.Errorf("cookie still %d bytes after dropping ID token (limit %d)", len(cookie.Value), maxCookieSize)
	}
}

// hugeUser builds a user whose signed cookie can't fit in one cookie even after
// the ID token is dropped, forcing the chunked path. Many large groups do it.
func hugeUser() *User {
	groups := make([]string, 300)
	for i := range groups {
		groups[i] = "org:platform:engineering:team-" + strings.Repeat("x", 20)
	}
	return &User{Username: "alice@example.com", Groups: groups}
}

func TestCreateSessionCookie_ChunksWhenTooLarge(t *testing.T) {
	secret := "test-secret"
	sid := NewSessionID()

	cookies := CreateSessionCookie(hugeUser(), sid, "", secret, 1*time.Hour, true)
	if len(cookies) < 3 {
		t.Fatalf("expected chunked cookies (chunks + meta), got %d", len(cookies))
	}

	var sawMeta bool
	for _, c := range cookies {
		if len(c.Value) > maxCookieSize {
			t.Errorf("chunk %q is %d bytes, exceeds limit %d", c.Name, len(c.Value), maxCookieSize)
		}
		if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
			t.Errorf("chunk %q missing standard attributes (HttpOnly/Secure/SameSite)", c.Name)
		}
		if c.Name == DefaultCookieName+"_chunks" {
			sawMeta = true
		}
	}
	if !sawMeta {
		t.Error("chunked cookie set is missing the _chunks meta-cookie")
	}

	// Reassembly round-trips back to the original identity.
	req := httptest.NewRequest("GET", "/", nil)
	addCookies(req, cookies)
	parsed := ParseSessionCookie(req, secret)
	if parsed == nil {
		t.Fatal("ParseSessionCookie returned nil for chunked cookies")
	}
	if parsed.User.Username != "alice@example.com" {
		t.Errorf("username = %q, want alice@example.com", parsed.User.Username)
	}
	if len(parsed.User.Groups) != 300 {
		t.Errorf("groups = %d, want 300", len(parsed.User.Groups))
	}
	if parsed.SID != sid {
		t.Errorf("SID = %q, want %q", parsed.SID, sid)
	}
}

func TestParseSessionCookie_DroppedChunkFails(t *testing.T) {
	secret := "test-secret"
	cookies := CreateSessionCookie(hugeUser(), NewSessionID(), "", secret, 1*time.Hour, false)

	// Drop the first chunk but keep the meta-cookie claiming the full count.
	req := httptest.NewRequest("GET", "/", nil)
	for _, c := range cookies {
		if c.Name == DefaultCookieName+"_chunk_0" {
			continue
		}
		req.AddCookie(c)
	}

	if ParseSessionCookie(req, secret) != nil {
		t.Error("ParseSessionCookie should return nil when a chunk is missing")
	}
}

func TestParseSessionCookie_TamperedChunkFails(t *testing.T) {
	secret := "test-secret"
	cookies := CreateSessionCookie(hugeUser(), NewSessionID(), "", secret, 1*time.Hour, false)

	req := httptest.NewRequest("GET", "/", nil)
	for _, c := range cookies {
		if c.Name == DefaultCookieName+"_chunk_1" && len(c.Value) > 0 {
			// Flip a byte in a middle chunk; the HMAC over the reassembled value must fail.
			b := []byte(c.Value)
			if b[0] == 'A' {
				b[0] = 'B'
			} else {
				b[0] = 'A'
			}
			c.Value = string(b)
		}
		req.AddCookie(c)
	}

	if ParseSessionCookie(req, secret) != nil {
		t.Error("ParseSessionCookie should return nil when a chunk is tampered")
	}
}

func TestParseSessionCookie_ChunkCountCapped(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: DefaultCookieName + "_chunks", Value: "100000"})

	if ParseSessionCookie(req, "secret") != nil {
		t.Error("ParseSessionCookie should reject an out-of-range chunk count")
	}
}

func TestParseSessionCookie_PrefersMainCookieOverStaleChunks(t *testing.T) {
	secret := "test-secret"
	// A valid single cookie for a shrunk session...
	main := singleCookie(t, CreateSessionCookie(&User{Username: "bob"}, NewSessionID(), "", secret, 1*time.Hour, false))
	// ...with stale chunk cookies from a previous larger session still in the browser.
	stale := CreateSessionCookie(hugeUser(), NewSessionID(), "", secret, 1*time.Hour, false)

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(main)
	addCookies(req, stale)

	parsed := ParseSessionCookie(req, secret)
	if parsed == nil {
		t.Fatal("ParseSessionCookie returned nil despite a valid main cookie")
	}
	if parsed.User.Username != "bob" {
		t.Errorf("username = %q, want bob (main cookie should win over stale chunks)", parsed.User.Username)
	}
}

func TestClearSessionCookie_ClearsChunks(t *testing.T) {
	secret := "test-secret"
	cookies := CreateSessionCookie(hugeUser(), NewSessionID(), "", secret, 1*time.Hour, false)

	// Simulate the browser sending the chunked session back on logout.
	req := httptest.NewRequest("POST", "/auth/logout", nil)
	addCookies(req, cookies)

	cleared := ClearSessionCookie(req)

	// Every set cookie (chunks + meta + main) must get a clearing counterpart.
	clearedNames := map[string]bool{}
	for _, c := range cleared {
		if c.MaxAge != -1 || c.Value != "" {
			t.Errorf("clearing cookie %q should have MaxAge=-1 and empty value, got MaxAge=%d value=%q", c.Name, c.MaxAge, c.Value)
		}
		clearedNames[c.Name] = true
	}
	if !clearedNames[DefaultCookieName] {
		t.Error("logout did not clear the main cookie")
	}
	if !clearedNames[DefaultCookieName+"_chunks"] {
		t.Error("logout did not clear the _chunks meta-cookie")
	}
	for _, c := range cookies {
		if strings.Contains(c.Name, "_chunk_") && !clearedNames[c.Name] {
			t.Errorf("logout did not clear chunk cookie %q — session would survive logout", c.Name)
		}
	}

	// After applying the clears, the (now empty) chunk cookies must not reassemble.
	logoutReq := httptest.NewRequest("GET", "/", nil)
	addCookies(logoutReq, cleared)
	if ParseSessionCookie(logoutReq, secret) != nil {
		t.Error("session still parses after logout cleared the chunk cookies")
	}
}
