// Package openobserve scopes Prometheus API traffic to one cluster when the
// metrics store is an OpenObserve organization shared by many clusters.
//
// OpenObserve answers PromQL at <base>/api/<org>/prometheus/api/v1/... with
// basic auth, and lowercases metric and label names on ingest. Every query
// sent through this package carries the cluster's label matcher, and every
// series returned without it is dropped.
package openobserve

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/skyhook-io/radar/pkg/prom"
)

// Connection describes one OpenObserve organization reachable over HTTP(S).
type Connection struct {
	// BaseURL is https://host[:port][/prefix]; a trailing slash is ignored.
	BaseURL string
	Org     string
	User    string
	// Token is the resolved secret, never a reference to one.
	Token string
	// CACertPEM is an optional PEM bundle trusted in addition to the system roots.
	CACertPEM string
}

var orgPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// Validate reports the first problem that would make the connection unusable
// or unsafe to build a URL or Authorization header from.
func (c Connection) Validate() error {
	if strings.TrimSpace(c.BaseURL) == "" {
		return errors.New("openobserve: base URL is required")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return fmt.Errorf("openobserve: invalid base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("openobserve: base URL must use http or https")
	}
	if u.Host == "" {
		return errors.New("openobserve: base URL has no host")
	}
	if u.User != nil {
		return errors.New("openobserve: base URL must not contain credentials")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return errors.New("openobserve: base URL must not contain a query string")
	}
	if u.Fragment != "" || strings.Contains(c.BaseURL, "#") {
		return errors.New("openobserve: base URL must not contain a fragment")
	}
	if !orgPattern.MatchString(c.Org) {
		return errors.New("openobserve: organization must be non-empty and contain only letters, digits, '_', '-' or '.'")
	}
	if c.User == "" {
		return errors.New("openobserve: user is required")
	}
	// Basic auth splits user from secret at the first colon.
	if strings.Contains(c.User, ":") {
		return errors.New("openobserve: user must not contain ':'")
	}
	if c.Token == "" {
		return errors.New("openobserve: token is required")
	}
	if hasControl(c.User) || hasControl(c.Token) {
		return errors.New("openobserve: user and token must not contain control characters")
	}
	return nil
}

func hasControl(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}

// PrometheusURL is the Prometheus-compatible API root for the organization.
func (c Connection) PrometheusURL() string {
	return strings.TrimRight(c.BaseURL, "/") + "/api/" + url.PathEscape(c.Org) + "/prometheus"
}

// AuthHeader is the value for the Authorization header.
func (c Connection) AuthHeader() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(c.User+":"+c.Token))
}

// TLSConfig trusts the system roots plus CACertPEM, when set.
func (c Connection) TLSConfig() (*tls.Config, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if strings.TrimSpace(c.CACertPEM) != "" && !pool.AppendCertsFromPEM([]byte(c.CACertPEM)) {
		return nil, errors.New("openobserve: CA bundle contains no valid PEM certificates")
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, nil
}

// NewHTTPTransport validates the connection and returns a direct transport to
// its Prometheus API with basic auth and the connection's TLS trust. It does
// not scope queries; wrap it in a RewritingTransport for that.
func (c Connection) NewHTTPTransport(timeout time.Duration) (*prom.HTTPTransport, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	tlsConfig, err := c.TLSConfig()
	if err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.TLSClientConfig = tlsConfig
	t := prom.NewHTTPTransport(c.PrometheusURL(), "", &http.Client{Timeout: timeout, Transport: RateLimitRoundTripper{Next: base}})
	t.Headers = map[string]string{"Authorization": c.AuthHeader()}
	return t, nil
}
