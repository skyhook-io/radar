// Package argoapi is a minimal REST client for the Argo CD API server. It is
// a pure package with no internal/ imports so it can be consumed both by
// Radar's wiring and by external hosts with their own credential stores.
package argoapi

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	// ErrUnauthorized is wrapped into errors returned for 401/403 responses.
	ErrUnauthorized = errors.New("argoapi: unauthorized")
	// ErrNotFound is wrapped into errors returned for 404 responses.
	ErrNotFound = errors.New("argoapi: not found")
)

// Options configures a Client.
type Options struct {
	// BaseURL is the Argo CD API server root, e.g. "https://argocd.example.com".
	BaseURL string
	// Token is sent as "Authorization: Bearer <token>" when non-empty.
	Token string
	// InsecureSkipTLSVerify disables TLS certificate verification for this
	// client only. Ignored when HTTPClient is provided.
	InsecureSkipTLSVerify bool
	// Timeout applies to the default HTTP client (default 15s). Ignored when
	// HTTPClient is provided.
	Timeout time.Duration
	// HTTPClient overrides the default client entirely — useful for tests and
	// port-forward transports. The caller owns TLS and timeout behavior.
	HTTPClient *http.Client
	// Logger is optional; if set, the client emits one line per request.
	Logger func(format string, args ...any)
}

// Client is an Argo CD API server REST client. Construct with New.
type Client struct {
	baseURL string
	token   string
	hc      *http.Client
	logf    func(format string, args ...any)
}

// New builds a Client from opts. See Options for defaults.
func New(opts Options) *Client {
	hc := opts.HTTPClient
	if hc == nil {
		timeout := opts.Timeout
		if timeout <= 0 {
			timeout = 15 * time.Second
		}
		hc = &http.Client{Timeout: timeout}
		if opts.InsecureSkipTLSVerify {
			transport := http.DefaultTransport.(*http.Transport).Clone()
			if transport.TLSClientConfig == nil {
				transport.TLSClientConfig = &tls.Config{}
			}
			transport.TLSClientConfig.InsecureSkipVerify = true
			hc.Transport = transport
		}
	}
	// Refuse redirects that change origin (scheme/host/port). Go's default policy
	// re-sends the Authorization header to same-domain and subdomain targets, so a
	// misconfigured or hostile Argo endpoint could redirect to http:// or another
	// host and capture the bearer token. Same-origin path redirects stay allowed.
	// Only set when the caller didn't supply its own policy.
	if hc.CheckRedirect == nil {
		hc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if len(via) == 0 {
				return nil
			}
			orig := via[0].URL
			if req.URL.Scheme != orig.Scheme || req.URL.Host != orig.Host {
				return fmt.Errorf("argoapi: refusing cross-origin redirect to %s (token would be exposed)", req.URL.Host)
			}
			return nil
		}
	}
	logf := opts.Logger
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Client{
		baseURL: strings.TrimRight(opts.BaseURL, "/"),
		token:   opts.Token,
		hc:      hc,
		logf:    logf,
	}
}

// ManagedResources returns the managed-resource diffs for an application via
// GET /api/v1/applications/{name}/managed-resources.
func (c *Client) ManagedResources(ctx context.Context, q ManagedResourcesQuery) ([]ResourceDiff, error) {
	if q.AppName == "" {
		return nil, errors.New("argoapi: ManagedResources requires AppName")
	}
	params := url.Values{}
	setNonEmpty(params, "appNamespace", q.AppNamespace)
	setNonEmpty(params, "project", q.Project)
	setNonEmpty(params, "group", q.Group)
	setNonEmpty(params, "kind", q.Kind)
	setNonEmpty(params, "namespace", q.Namespace)
	setNonEmpty(params, "name", q.Name)

	var out struct {
		Items []ResourceDiff `json:"items"`
	}
	path := "/api/v1/applications/" + url.PathEscape(q.AppName) + "/managed-resources"
	if err := c.get(ctx, path, params, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// RevisionMetadata returns the Git commit metadata for a revision via
// GET /api/v1/applications/{name}/revisions/{revision}/metadata.
func (c *Client) RevisionMetadata(ctx context.Context, q RevisionMetadataQuery) (*RevisionMetadata, error) {
	if q.AppName == "" || q.Revision == "" {
		return nil, errors.New("argoapi: RevisionMetadata requires AppName and Revision")
	}
	params := url.Values{}
	setNonEmpty(params, "appNamespace", q.AppNamespace)
	setNonEmpty(params, "project", q.Project)
	setNonEmpty(params, "sourceIndex", q.SourceIndex)

	var out RevisionMetadata
	path := "/api/v1/applications/" + url.PathEscape(q.AppName) +
		"/revisions/" + url.PathEscape(q.Revision) + "/metadata"
	if err := c.get(ctx, path, params, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Repositories returns the configured repositories and Argo CD's cached
// connection state for each, via GET /api/v1/repositories.
func (c *Client) Repositories(ctx context.Context) ([]Repository, error) {
	var out struct {
		Items []Repository `json:"items"`
	}
	if err := c.get(ctx, "/api/v1/repositories", nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// UserInfo probes token validity via GET /api/v1/session/userinfo.
func (c *Client) UserInfo(ctx context.Context) (*UserInfo, error) {
	var out UserInfo
	if err := c.get(ctx, "/api/v1/session/userinfo", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Version probes server reachability via GET /api/version and returns the
// reported version string. The endpoint may not require auth.
func (c *Client) Version(ctx context.Context) (string, error) {
	var out struct {
		Version string `json:"Version"`
	}
	if err := c.get(ctx, "/api/version", nil, &out); err != nil {
		return "", err
	}
	return out.Version, nil
}

// maxResponseBody caps reads generously — managed-resources payloads embed
// full manifests for every resource of an application.
const maxResponseBody = 32 << 20

func (c *Client) get(ctx context.Context, path string, params url.Values, out any) error {
	full := c.baseURL + path
	if len(params) > 0 {
		full += "?" + params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return fmt.Errorf("argoapi: build request for %s: %w", path, err)
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		c.logf("argoapi: GET %s failed: %v", path, err)
		return fmt.Errorf("argoapi: GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return fmt.Errorf("argoapi: read response from %s: %w", path, err)
	}
	c.logf("argoapi: GET %s → %d (%d bytes)", path, resp.StatusCode, len(body))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError(resp.StatusCode, path, body)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("argoapi: parse response from %s: %w", path, err)
	}
	return nil
}

func setNonEmpty(params url.Values, key, value string) {
	if value != "" {
		params.Set(key, value)
	}
}

func statusError(status int, path string, body []byte) error {
	msg := errorMessage(body)
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: %d from %s: %s", ErrUnauthorized, status, path, msg)
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s: %s", ErrNotFound, path, msg)
	}
	return fmt.Errorf("argoapi: %s returned %d: %s", path, status, msg)
}

// errorMessage extracts a short, safe message from an Argo CD error body
// ({"error": "...", "message": "..."}). The raw body is deliberately NOT used
// as a fallback: on a diff/render failure it can contain manifest or Secret
// content, and this string ends up in server logs. When the body isn't Argo's
// structured error shape we return a generic marker instead. The structured
// message is capped — Argo's API errors are short ("application not found"),
// so a long value is suspect.
func errorMessage(body []byte) string {
	var e struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &e) == nil {
		if m := strings.TrimSpace(e.Message); m != "" {
			return capMessage(m)
		}
		if m := strings.TrimSpace(e.Error); m != "" {
			return capMessage(m)
		}
	}
	return "(unstructured error body omitted)"
}

func capMessage(s string) string {
	const maxErrMessage = 200
	if len(s) > maxErrMessage {
		return s[:maxErrMessage] + "…"
	}
	return s
}
