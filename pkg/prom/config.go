package prom

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// ErrHeadersRequireURL is returned instead of running discovery when
// Prometheus headers are configured without a URL. Headers carry credentials,
// and discovery probes every candidate Service that looks like Prometheus, so
// without an explicit URL those credentials would be sent to endpoints the
// operator never named.
var ErrHeadersRequireURL = errors.New("Prometheus headers are configured but no Prometheus URL is set; " +
	"set --prometheus-url (Helm: traffic.prometheusUrl) or remove the headers — credentials are never sent to auto-discovered endpoints")

// HeadersRequireURL reports whether a configuration would be refused by
// discovery: headers present, URL absent. Every surface that can run
// discovery applies this one rule.
func HeadersRequireURL(manualURL string, headers map[string]string) bool {
	return strings.TrimSpace(manualURL) == "" && len(headers) > 0
}

func ValidateHeaders(headers map[string]string) error {
	seen := make(map[string]bool, len(headers))
	for key, value := range headers {
		if !httpguts.ValidHeaderFieldName(key) {
			return fmt.Errorf("invalid prometheus header name %q (must be RFC 7230 tokens)", key)
		}
		if !httpguts.ValidHeaderFieldValue(value) {
			return fmt.Errorf("invalid value for prometheus header %q (control characters not allowed)", key)
		}
		canonical := http.CanonicalHeaderKey(key)
		if seen[canonical] {
			return fmt.Errorf("duplicate prometheus header %q (header names are case-insensitive)", canonical)
		}
		seen[canonical] = true
	}
	return nil
}
