package context

import (
	"strings"
	"testing"
)

func TestRedactSecrets_OpenAIKey(t *testing.T) {
	input := "Using API key sk-abc123def456ghi789jkl012mno345pqr678stu901 for requests"
	result := RedactSecrets(input)
	if strings.Contains(result, "sk-abc123") {
		t.Errorf("OpenAI key not redacted: %s", result)
	}
	if !strings.Contains(result, "[REDACTED]") {
		t.Errorf("Expected [REDACTED] placeholder, got: %s", result)
	}
}

func TestRedactSecrets_GitHubPAT(t *testing.T) {
	input := "token=ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij"
	result := RedactSecrets(input)
	if strings.Contains(result, "ghp_") {
		t.Errorf("GitHub PAT not redacted: %s", result)
	}
}

func TestRedactSecrets_AWSAccessKey(t *testing.T) {
	input := "AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE"
	result := RedactSecrets(input)
	if strings.Contains(result, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("AWS key not redacted: %s", result)
	}
}

func TestRedactSecrets_BearerToken(t *testing.T) {
	input := "Authorization: Bearer eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJrdWJl"
	result := RedactSecrets(input)
	if strings.Contains(result, "eyJhbGci") {
		t.Errorf("Bearer token not redacted: %s", result)
	}
	if !strings.Contains(result, "Bearer [REDACTED]") {
		t.Errorf("Expected 'Bearer [REDACTED]', got: %s", result)
	}
}

func TestRedactSecrets_Password(t *testing.T) {
	input := "password=supersecretpassword123"
	result := RedactSecrets(input)
	if strings.Contains(result, "supersecret") {
		t.Errorf("Password not redacted: %s", result)
	}
	if !strings.Contains(result, "password=") {
		t.Errorf("Expected password key to be preserved, got: %s", result)
	}
}

func TestRedactSecrets_Base64Block(t *testing.T) {
	// Long base64 string (>50 chars)
	input := "data: " + strings.Repeat("YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXo=", 3)
	result := RedactSecrets(input)
	if strings.Contains(result, "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXo") {
		t.Errorf("Base64 block not redacted: %s", result)
	}
}

func TestRedactSecrets_SafeContent(t *testing.T) {
	input := "Normal log line: pod my-app-abc123 started successfully"
	result := RedactSecrets(input)
	if result != input {
		t.Errorf("Safe content was modified: %q → %q", input, result)
	}
}

func TestRedactSecrets_EmptyString(t *testing.T) {
	result := RedactSecrets("")
	if result != "" {
		t.Errorf("Expected empty string, got: %s", result)
	}
}

func TestRedactSecrets_GitHubAppToken(t *testing.T) {
	input := "token: ghs_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij"
	result := RedactSecrets(input)
	if strings.Contains(result, "ghs_") {
		t.Errorf("GitHub app token not redacted: %s", result)
	}
}

func TestRedactSecrets_MultipleSecrets(t *testing.T) {
	input := "key1=sk-abc123def456ghi789jkl012mno and key2=AKIAIOSFODNN7EXAMPLE"
	result := RedactSecrets(input)
	if strings.Contains(result, "sk-abc") || strings.Contains(result, "AKIAIOSF") {
		t.Errorf("Not all secrets redacted: %s", result)
	}
}

func TestRedactInlineSecrets_KeyAware(t *testing.T) {
	// A Traefik Middleware basicAuth with inline htpasswd users (under a
	// sensitive key) plus benign config that must survive untouched.
	spec := map[string]any{
		"basicAuth": map[string]any{
			"users": []any{
				"admin:$apr1$abc123$Zk9x.longhashvaluehere0000",
				"ops:$2y$10$anotherbcrypthashthatisquitelong00",
			},
			"realm": "protected",
		},
		"headers": map[string]any{
			"customRequestHeaders": map[string]any{
				"X-Build-Hash": "9f8e7d6c5b4a3f2e1d0c9b8a7f6e5d4c3b2a1908", // SHA-like, must survive
			},
		},
	}
	RedactInlineSecrets(spec)

	users := spec["basicAuth"].(map[string]any)["users"].([]any)
	for _, u := range users {
		if strings.Contains(u.(string), "$apr1$") || strings.Contains(u.(string), "$2y$") {
			t.Errorf("inline htpasswd hash not redacted: %v", u)
		}
	}
	if realm := spec["basicAuth"].(map[string]any)["realm"]; realm != "protected" {
		t.Errorf("benign realm should survive, got %v", realm)
	}
	hash := spec["headers"].(map[string]any)["customRequestHeaders"].(map[string]any)["X-Build-Hash"]
	if hash != "9f8e7d6c5b4a3f2e1d0c9b8a7f6e5d4c3b2a1908" {
		t.Errorf("SHA-like config value was over-redacted: %v", hash)
	}
}

func TestRedactInlineSecrets_HighConfidenceValueAnywhere(t *testing.T) {
	// A token-shaped value under a non-sensitive key still gets caught by the
	// high-confidence patterns (but NOT the broad base64 rule).
	spec := map[string]any{
		"address": "https://auth.example.com?t=ghp_0123456789abcdef0123456789abcdef0123",
	}
	RedactInlineSecrets(spec)
	if strings.Contains(spec["address"].(string), "ghp_0123456789") {
		t.Errorf("GitHub PAT in a non-sensitive field not redacted: %v", spec["address"])
	}
}

func TestRedactInlineSecrets_PreservesSecretReferences(t *testing.T) {
	// Reference keys hold the NAME of a Secret, not secret material — they must
	// survive so a reader can diagnose a missing/wrong reference. Value-bearing
	// keys (password) must still be redacted.
	spec := map[string]any{
		"basicAuth":      map[string]any{"secret": "web-users"},     // Traefik: a reference
		"tls":            map[string]any{"secretName": "tls-cert"},  // reference
		"rootCAsSecrets": []any{"internal-ca"},                      // reference list
		"oauth":          map[string]any{"password": "hunter2pass"}, // inline value → redact
	}
	RedactInlineSecrets(spec)

	if got := spec["basicAuth"].(map[string]any)["secret"]; got != "web-users" {
		t.Errorf("basicAuth.secret reference should survive, got %v", got)
	}
	if got := spec["tls"].(map[string]any)["secretName"]; got != "tls-cert" {
		t.Errorf("secretName reference should survive, got %v", got)
	}
	if got := spec["rootCAsSecrets"].([]any)[0]; got != "internal-ca" {
		t.Errorf("rootCAsSecrets reference should survive, got %v", got)
	}
	if got := spec["oauth"].(map[string]any)["password"]; got != "[REDACTED]" {
		t.Errorf("inline password value must be redacted, got %v", got)
	}
}

func TestRedactInlineSecrets_PlaintextUsersKey(t *testing.T) {
	// `users` is a credential key (htpasswd entries). Even a plaintext value
	// that matches no high-confidence pattern must be redacted by key.
	spec := map[string]any{
		"basicAuth": map[string]any{"users": []any{"admin:hunter2"}},
	}
	RedactInlineSecrets(spec)
	if got := spec["basicAuth"].(map[string]any)["users"].([]any)[0]; got != "[REDACTED]" {
		t.Errorf("plaintext users entry must be redacted by key, got %v", got)
	}
}
