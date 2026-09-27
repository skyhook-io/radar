package app

import (
	"errors"
	"net/url"
	"strings"

	"github.com/skyhook-io/radar/pkg/prom"
)

// validateStartupPrometheusURL is the acceptance --prometheus-url has always
// had: the transport honors userinfo and query parameters, so launch flags and
// Helm values may carry them. Settings saves use the stricter prom.ValidateBaseURL.
func validateStartupPrometheusURL(raw string) error {
	if u, err := url.Parse(raw); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("must be a valid HTTP(S) URL (e.g., http://prometheus-server.monitoring:9090)")
	}
	return nil
}

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
