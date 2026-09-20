package app

import (
	"errors"
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

func ResolvePrometheusHeaders(headers, headersFromEnv map[string]string) (map[string]string, error) {
	return prom.ResolveHeaders(headers, headersFromEnv)
}
func ValidEnvVarName(s string) bool {
	return prom.ValidEnvVarName(s)
}
