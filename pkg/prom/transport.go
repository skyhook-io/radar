package prom

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Transport is the pluggable HTTP transport used by Client to issue requests
// to a Prometheus HTTP API. Implementations decide how the request physically
// reaches Prometheus — typically either direct HTTP against a known URL
// (in-cluster, or a kubectl port-forwarded localhost) or a tunneled proxy
// transport that forwards requests through some external broker to an
// in-cluster Prometheus.
//
// Transport is responsible for returning the raw upstream body bytes. Parsing
// is the Client's concern.
type Transport interface {
	Do(ctx context.Context, method, path string, params url.Values) ([]byte, error)

	// Address returns a human-readable identifier for this transport, used
	// for status reporting and error messages — typically the base URL, or
	// a short description of the proxy path for tunneled transports.
	Address() string
}

// HTTPTransport is a direct-HTTP Transport. It targets BaseURL + BasePath +
// the request path, and uses HTTPClient to send the request.
//
// BasePath is an optional prefix applied before Prometheus API paths and is
// useful for vmselect-style deployments where the API lives under e.g.
// "/select/0/prometheus".
//
// Headers, if non-empty, are applied to every request after the default
// Accept header, so callers may override Accept by setting it here. Typical
// uses are Authorization: Bearer ... and tenant headers like X-Scope-OrgID.
type HTTPTransport struct {
	BaseURL    string
	BasePath   string
	HTTPClient *http.Client
	Headers    map[string]string
}

// NewHTTPTransport constructs an HTTPTransport with a default 10-second
// timeout if none is provided.
func NewHTTPTransport(baseURL, basePath string, httpClient *http.Client) *HTTPTransport {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &HTTPTransport{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		BasePath:   basePath,
		HTTPClient: httpClient,
	}
}

// Do issues a request and returns the response body bytes. Non-2xx status
// codes yield a *HTTPError; callers can use errors.As to extract the
// status code and upstream body (Probe distinguishes 401/403 from other
// transport errors this way, for example).
func (t *HTTPTransport) Do(ctx context.Context, method, path string, params url.Values) ([]byte, error) {
	full := t.BaseURL + t.BasePath + path
	if len(params) > 0 {
		if strings.Contains(full, "?") {
			full = full + "&" + params.Encode()
		} else {
			full = full + "?" + params.Encode()
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, full, nil)
	if err != nil {
		return nil, fmt.Errorf("prom.HTTPTransport: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range t.Headers {
		req.Header.Set(k, v)
	}

	resp, err := t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prom.HTTPTransport: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10 MiB cap
	if err != nil {
		return nil, fmt.Errorf("prom.HTTPTransport: read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPError{StatusCode: resp.StatusCode, URL: full, Body: body}
	}
	return body, nil
}

// Address returns the effective base URL for diagnostics.
func (t *HTTPTransport) Address() string {
	return t.BaseURL + t.BasePath
}

// HTTPError is returned when Prometheus responds with a non-2xx status.
type HTTPError struct {
	StatusCode int
	URL        string
	Body       []byte
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("prometheus returned %d for %s: %s", e.StatusCode, e.URL, string(e.Body))
}
