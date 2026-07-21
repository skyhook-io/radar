package context

import (
	"regexp"
	"strings"
)

// highConfidenceSecretPatterns match strongly-typed secret shapes (prefixed
// tokens, AWS key IDs, bearer headers, keyed password values). They have a low
// false-positive rate, so they're safe to run over arbitrary CRD string values.
var highConfidenceSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`),                       // OpenAI API keys
	regexp.MustCompile(`ghp_[A-Za-z0-9]{36}`),                       // GitHub personal access tokens
	regexp.MustCompile(`gho_[A-Za-z0-9]{36}`),                       // GitHub OAuth tokens
	regexp.MustCompile(`ghs_[A-Za-z0-9]{36}`),                       // GitHub App installation tokens
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{22,}`),              // GitHub fine-grained PATs
	regexp.MustCompile(`AKIA[A-Z0-9]{16}`),                          // AWS access key IDs
	regexp.MustCompile(`Bearer\s+[A-Za-z0-9\-._~+/]{20,}`),          // Bearer tokens
	regexp.MustCompile(`(?i)password[=:]\s*\S{8,}`),                 // password= or password: values
	regexp.MustCompile(`\$(?:apr1|2[aby]|5|6)\$[./A-Za-z0-9$]{8,}`), // htpasswd/crypt hashes (basicAuth users)
}

// base64SecretPattern is a broad catch-all for base64 blobs. It earns its keep
// in free-text (logs, env values) but over-redacts when applied to arbitrary
// CRD fields (it eats SHA-like IDs, config hashes, generated names), so the
// spec walker deliberately does NOT use it — see RedactInlineSecrets.
var base64SecretPattern = regexp.MustCompile(`[A-Za-z0-9+/=]{50,}`)

// secretPatterns is the full set used for free-text redaction (logs, env values).
var secretPatterns = append(append([]*regexp.Regexp{}, highConfidenceSecretPatterns...), base64SecretPattern)

// sensitiveValueKeys are exact key names (normalized: lowercased, '-'/'_'
// stripped) whose string value is inline secret MATERIAL. Deliberately matched
// exactly and scoped to value-bearing names — NOT substrings — so reference
// keys like secretName, secretRef, rootCAsSecrets, and secretKeyRef survive:
// those hold the *name* of a Secret, which the operator/LLM needs to diagnose a
// missing or wrong reference. (A bare `secret` is also a reference in Traefik
// basicAuth, so it is intentionally absent here.)
// Keep in sync with SECRET_VALUE_KEYS in TraefikMiddlewareRenderer.tsx (the
// frontend mirror; no shared source across the language boundary). `users`
// covers htpasswd-style basicAuth entries; the broad base64 catch-all is
// deliberately NOT applied to arbitrary CRD values here (it would redact
// SHA/IDs/config hashes) — see redactNode.
var sensitiveValueKeys = map[string]bool{
	"password": true, "passwd": true, "passphrase": true, "token": true,
	"clientsecret": true, "privatekey": true, "apikey": true, "apitoken": true,
	"accesstoken": true, "sessiontoken": true, "secretaccesskey": true,
	"secretkey": true, "authtoken": true, "bearertoken": true, "users": true,
}

func isSensitiveKey(key string) bool {
	norm := strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(key), "-", ""), "_", "")
	return sensitiveValueKeys[norm]
}

func IsSensitiveEnvName(name string) bool {
	lower := strings.ToLower(name)
	compact := strings.NewReplacer("-", "", "_", "", ".", "", "/", "").Replace(lower)
	return strings.Contains(lower, "password") || strings.Contains(lower, "passwd") ||
		strings.Contains(lower, "token") || strings.Contains(lower, "secret") ||
		strings.Contains(lower, "credential") ||
		strings.Contains(lower, "api_key") || strings.Contains(lower, "apikey") ||
		strings.Contains(lower, "accesskey") || strings.Contains(lower, "privatekey") ||
		strings.Contains(lower, "private_key") ||
		strings.Contains(compact, "apikey") || strings.Contains(compact, "accesskey") ||
		strings.Contains(compact, "privatekey") || strings.Contains(compact, "clientsecret")
}

func applyPatterns(text string, patterns []*regexp.Regexp) string {
	result := text
	for _, pattern := range patterns {
		result = pattern.ReplaceAllStringFunc(result, func(match string) string {
			// For Bearer tokens, preserve the "Bearer " prefix
			if strings.HasPrefix(match, "Bearer ") || strings.HasPrefix(match, "bearer ") {
				return match[:7] + "[REDACTED]"
			}
			// For password= patterns, preserve the key
			lower := strings.ToLower(match)
			if strings.HasPrefix(lower, "password") {
				eqIdx := strings.IndexAny(match, "=:")
				if eqIdx >= 0 {
					return match[:eqIdx+1] + " [REDACTED]"
				}
			}
			return "[REDACTED]"
		})
	}
	return result
}

// RedactSecrets replaces common secret patterns in text with [REDACTED].
// This is defense-in-depth — it catches obvious patterns but won't catch everything.
func RedactSecrets(text string) string {
	if text == "" {
		return text
	}
	return applyPatterns(text, secretPatterns)
}

// RedactInlineSecrets walks an unstructured subtree (a CRD's spec/status) in
// place, redacting inline secret-shaped values. Key-aware: string values under
// a sensitive key name are fully redacted; every other string value gets only
// the high-confidence patterns (NOT the broad base64 rule), so legitimate
// config — hashes, IDs, match expressions — survives. Closes the CRD-spec gap:
// no value-level redaction reached unstructured specs before.
func RedactInlineSecrets(node any) {
	redactNode(node, false)
}

func redactNode(node any, keySensitive bool) any {
	switch v := node.(type) {
	case map[string]any:
		for k, val := range v {
			v[k] = redactNode(val, keySensitive || isSensitiveKey(k))
		}
		return v
	case []any:
		for i, item := range v {
			v[i] = redactNode(item, keySensitive)
		}
		return v
	case string:
		if v == "" {
			return v
		}
		if keySensitive {
			return "[REDACTED]"
		}
		return applyPatterns(v, highConfidenceSecretPatterns)
	default:
		return node
	}
}
