package app

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/skyhook-io/radar/pkg/prom"
)

func ValidatePrometheusHeaderDestination(savedURL, launchURL string, inheritsHeaders bool) error {
	// A header-only file can intentionally pair with a deployment's URL flag.
	// Once the file names a server, its credentials must stay bound to it.
	if !inheritsHeaders || strings.TrimSpace(savedURL) == "" {
		return nil
	}
	saved, savedOK := prom.NormalizeOrigin(savedURL)
	launch, launchOK := prom.NormalizeOrigin(launchURL)
	if savedOK && launchOK && saved == launch {
		return nil
	}
	return errors.New("refusing to send headers from ~/.radar/config.json to a different --prometheus-url; update the saved URL and headers together before launching")
}

// ResolvePrometheusHeaders combines literal headers with headers sourced from
// environment variables. Env-sourced entries fail closed when the variable is
// unset so auth-protected Prometheus backends do not silently receive bad creds.
func ResolvePrometheusHeaders(headers, headersFromEnv map[string]string) (map[string]string, error) {
	if len(headers) == 0 && len(headersFromEnv) == 0 {
		return nil, nil
	}
	if err := prom.ValidateHeaders(headers); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(headers)+len(headersFromEnv))
	for key, val := range headers {
		out[key] = val
	}
	for key, envName := range headersFromEnv {
		key = strings.TrimSpace(key)
		envName = strings.TrimSpace(envName)
		if key == "" {
			return nil, fmt.Errorf("empty prometheus header key for env var %q", envName)
		}
		for existing := range out {
			if strings.EqualFold(existing, key) {
				return nil, fmt.Errorf("prometheus header %q is configured from multiple sources", key)
			}
		}
		if !ValidEnvVarName(envName) {
			return nil, fmt.Errorf("invalid env var name %q for prometheus header %q", envName, key)
		}
		val, ok := os.LookupEnv(envName)
		if !ok {
			return nil, fmt.Errorf("prometheus header %q references unset env var %q", key, envName)
		}
		out[key] = val
	}
	if err := prom.ValidateHeaders(out); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
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
