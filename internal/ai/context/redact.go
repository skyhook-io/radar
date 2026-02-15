package context

import (
	"regexp"
	"strings"
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`),                    // OpenAI API keys
	regexp.MustCompile(`ghp_[A-Za-z0-9]{36}`),                    // GitHub personal access tokens
	regexp.MustCompile(`gho_[A-Za-z0-9]{36}`),                    // GitHub OAuth tokens
	regexp.MustCompile(`ghs_[A-Za-z0-9]{36}`),                    // GitHub App installation tokens
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{22,}`),           // GitHub fine-grained PATs
	regexp.MustCompile(`AKIA[A-Z0-9]{16}`),                       // AWS access key IDs
	regexp.MustCompile(`Bearer\s+[A-Za-z0-9\-._~+/]{20,}`),      // Bearer tokens
	regexp.MustCompile(`(?i)password[=:]\s*\S{8,}`),              // password= or password: values
	regexp.MustCompile(`[A-Za-z0-9+/=]{50,}`),                   // Base64 blocks >50 chars
}

// RedactSecrets replaces common secret patterns in text with [REDACTED].
// This is defense-in-depth — it catches obvious patterns but won't catch everything.
func RedactSecrets(text string) string {
	if text == "" {
		return text
	}

	result := text
	for _, pattern := range secretPatterns {
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
