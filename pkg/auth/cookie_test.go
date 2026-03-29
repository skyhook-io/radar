package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCreateAndParseSessionCookie(t *testing.T) {
	secret := "test-secret-key"
	user := &User{Username: "alice", Groups: []string{"devs", "admins"}}
	ttl := 1 * time.Hour

	cookie := CreateSessionCookie(user, secret, ttl, false)

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
	if parsed.Username != "alice" {
		t.Errorf("username = %q, want %q", parsed.Username, "alice")
	}
	if len(parsed.Groups) != 2 || parsed.Groups[0] != "devs" || parsed.Groups[1] != "admins" {
		t.Errorf("groups = %v, want [devs admins]", parsed.Groups)
	}
}

func TestParseSessionCookie_WrongSecret(t *testing.T) {
	user := &User{Username: "alice"}
	cookie := CreateSessionCookie(user, "secret-1", 1*time.Hour, false)

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
	cookie := CreateSessionCookie(user, "secret", -1*time.Second, false)

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
	cookie := CreateSessionCookie(user, "secret", 1*time.Hour, false)

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
	cookie := CreateSessionCookie(user, "secret", 1*time.Hour, true)
	if !cookie.Secure {
		t.Error("cookie should be Secure when secure=true")
	}
}

func TestCreateSessionCookie_NoGroups(t *testing.T) {
	user := &User{Username: "bob"}
	cookie := CreateSessionCookie(user, "secret", 1*time.Hour, false)

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookie)

	parsed := ParseSessionCookie(req, "secret")
	if parsed == nil {
		t.Fatal("ParseSessionCookie returned nil")
	}
	if parsed.Username != "bob" {
		t.Errorf("username = %q, want %q", parsed.Username, "bob")
	}
	if len(parsed.Groups) != 0 {
		t.Errorf("groups = %v, want empty", parsed.Groups)
	}
}

func TestClearSessionCookie(t *testing.T) {
	cookie := ClearSessionCookie()
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
