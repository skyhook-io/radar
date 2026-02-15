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
