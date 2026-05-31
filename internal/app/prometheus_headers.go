package app

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// ResolvePrometheusHeaders combines literal headers with headers sourced from
// environment variables. Env-sourced entries fail closed when the variable is
// unset so auth-protected Prometheus backends do not silently receive bad creds.
func ResolvePrometheusHeaders(headers, headersFromEnv map[string]string) (map[string]string, error) {
	if len(headers) == 0 && len(headersFromEnv) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(headers)+len(headersFromEnv))
	for key, val := range headers {
		key = strings.TrimSpace(key)
		if err := validatePrometheusHeader(key, val); err != nil {
			return nil, err
		}
		out[key] = val
	}
	for key, envName := range headersFromEnv {
		key = strings.TrimSpace(key)
		envName = strings.TrimSpace(envName)
		if key == "" {
			return nil, fmt.Errorf("empty prometheus header key for env var %q", envName)
		}
		if _, exists := out[key]; exists {
			return nil, fmt.Errorf("prometheus header %q configured both as a literal value and from env", key)
		}
		if !ValidEnvVarName(envName) {
			return nil, fmt.Errorf("invalid env var name %q for prometheus header %q", envName, key)
		}
		val, ok := os.LookupEnv(envName)
		if !ok {
			return nil, fmt.Errorf("prometheus header %q references unset env var %q", key, envName)
		}
		if err := validatePrometheusHeader(key, val); err != nil {
			return nil, err
		}
		out[key] = val
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func validatePrometheusHeader(key, val string) error {
	if key == "" {
		return fmt.Errorf("empty prometheus header key")
	}
	if !httpguts.ValidHeaderFieldName(key) {
		return fmt.Errorf("invalid prometheus header name %q (must be RFC 7230 tokens)", key)
	}
	if !httpguts.ValidHeaderFieldValue(val) {
		return fmt.Errorf("invalid prometheus header value for %q (control characters not allowed)", key)
	}
	return nil
}

func ValidEnvVarName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r == '_' || ('A' <= r && r <= 'Z') || ('a' <= r && r <= 'z') {
			continue
		}
		if i > 0 && '0' <= r && r <= '9' {
			continue
		}
		return false
	}
	return true
}
