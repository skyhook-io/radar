package prom

import (
	"errors"
	"strings"
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
