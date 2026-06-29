package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

const (
	curlTimeout      = 10 * time.Second
	curlMaxBodyBytes = 512 * 1024 // cap the previewed response body at 512 KiB
)

// namespace/service names are DNS-1123 subdomains; the port must be numeric (we
// dial host:port directly). Both are validated before being spliced into the
// cluster-DNS target so a crafted value can't change what we connect to.
var (
	curlNameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)
	curlPortRe = regexp.MustCompile(`^[0-9]{1,5}$`)
)

type curlRequest struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`   // Service name
	Port      string `json:"port"`   // numeric port
	Scheme    string `json:"scheme"` // "http" (default) or "https"
	Path      string `json:"path"`   // request path, defaults to "/"
}

type curlResponse struct {
	Status     int               `json:"status"`
	StatusText string            `json:"statusText"` // canonical reason phrase, e.g. "OK", "Service Unavailable"
	DurationMs int64             `json:"durationMs"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
	Truncated  bool              `json:"truncated"`
	BodyBytes  int               `json:"bodyBytes"`
	// Error is set when the response was received but reading its body failed
	// (e.g. the target stalled until the timeout). Status/body still reflect
	// whatever arrived so a partial response isn't silently shown as success.
	Error string `json:"error,omitempty"`
}

// curlDialClient builds the client used to dial a Service directly. It sends no
// Kubernetes credentials (a plain HTTP client, unlike the apiserver services/proxy
// which forwards the caller's Authorization/Impersonate headers to the backend),
// disables redirect-following (don't chase a Service-controlled 3xx), and skips
// TLS verification (internal Services routinely serve self-signed/internal-CA
// certs and we're inspecting the response, not establishing trust — and nothing
// sensitive is sent).
func curlDialClient() *http.Client {
	return &http.Client{
		Timeout: curlTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // diagnostic curl; no creds sent
			DisableKeepAlives: true,
		},
	}
}

// friendlyDialError turns a Go dial/transport error into operator-readable text.
func friendlyDialError(err error) string {
	msg := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(msg, "context deadline exceeded") || strings.Contains(msg, "Client.Timeout"):
		return fmt.Sprintf("No response within %s — the Service may be down or not listening on this port.", curlTimeout)
	case strings.Contains(msg, "no such host"):
		return "Could not resolve the Service — check the name/namespace, or it may not exist."
	case strings.Contains(msg, "connection refused"):
		return "Connection refused — no pod is accepting connections on this port (the Service may have no ready endpoints)."
	default:
		return "Could not reach the Service: " + msg
	}
}

// authorizeCurl checks the caller may reach the Service. Direct-dial bypasses the
// apiserver, so we re-create the authorization it would have enforced for
// services/proxy via a SubjectAccessReview as the calling user. No auth (local
// self-host) means a single trusted operator — allow.
func (s *Server) authorizeCurl(ctx context.Context, r *http.Request, namespace, name string) (bool, error) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		return true, nil
	}
	client := k8s.GetClient()
	if client == nil {
		return false, fmt.Errorf("k8s client not initialized")
	}
	review := &authv1.SubjectAccessReview{
		Spec: authv1.SubjectAccessReviewSpec{
			User:   user.Username,
			Groups: user.Groups,
			ResourceAttributes: &authv1.ResourceAttributes{
				Namespace:   namespace,
				Verb:        "get",
				Resource:    "services",
				Subresource: "proxy",
				Name:        name,
			},
		},
	}
	result, err := client.AuthorizationV1().SubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	return result.Status.Allowed, nil
}

// handleCurlService issues a single server-side GET to an in-cluster Service by
// dialing it directly over cluster DNS. This is the Cloud-safe stand-in for
// port-forward when the question is "what does this endpoint return": the request
// originates in-cluster and the response flows back to the browser. Critically it
// dials the Service directly rather than via the apiserver services/proxy — the
// latter forwards the caller's Kubernetes credentials to the workload, which would
// leak Radar's token to anything you curl. Authorization is re-created with an
// explicit SubjectAccessReview.
func (s *Server) handleCurlService(w http.ResponseWriter, r *http.Request) {
	// Direct dial only works from inside the cluster network (Cloud / in-cluster).
	// Locally you'd port-forward instead.
	if !k8s.IsInCluster() {
		s.writeError(w, http.StatusBadRequest, "Service curl is only available when Radar runs in-cluster")
		return
	}

	var req curlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	req.Namespace = strings.TrimSpace(req.Namespace)
	req.Name = strings.TrimSpace(req.Name)
	req.Port = strings.TrimSpace(req.Port)
	if !curlNameRe.MatchString(req.Namespace) {
		s.writeError(w, http.StatusBadRequest, "invalid namespace")
		return
	}
	if !curlNameRe.MatchString(req.Name) {
		s.writeError(w, http.StatusBadRequest, "invalid service name")
		return
	}
	if !curlPortRe.MatchString(req.Port) {
		s.writeError(w, http.StatusBadRequest, "invalid port")
		return
	}

	scheme := strings.ToLower(strings.TrimSpace(req.Scheme))
	if scheme == "" {
		scheme = "http"
	}
	if scheme != "http" && scheme != "https" {
		s.writeError(w, http.StatusBadRequest, "scheme must be http or https")
		return
	}

	path := req.Path
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if strings.ContainsAny(path, "\r\n") {
		s.writeError(w, http.StatusBadRequest, "invalid path")
		return
	}

	if allowed, err := s.authorizeCurl(r.Context(), r, req.Namespace, req.Name); err != nil {
		s.writeError(w, http.StatusInternalServerError, "authorization check failed")
		return
	} else if !allowed {
		s.writeError(w, http.StatusForbidden, "You don't have permission to reach this Service (requires get services/proxy).")
		return
	}

	auth.AuditLog(r, req.Namespace, req.Name)

	// Resolve the Service from cache before dialing: this confirms it exists, that
	// the requested port is real, and — critically — lets us build the dial target
	// from the trusted cache object's fields rather than the raw request, so a
	// crafted name/namespace/port can never steer where we connect.
	cache := k8s.GetResourceCache()
	if cache == nil || cache.Services() == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Service cache not ready")
		return
	}
	svc, err := cache.Services().Services(req.Namespace).Get(req.Name)
	if err != nil {
		s.writeError(w, http.StatusNotFound, "Service not found")
		return
	}
	matchedPort := int32(-1)
	for _, p := range svc.Spec.Ports {
		if fmt.Sprintf("%d", p.Port) == req.Port {
			matchedPort = p.Port
			break
		}
	}
	if matchedPort < 0 {
		s.writeError(w, http.StatusBadRequest, "Service has no such port")
		return
	}

	// Dial the Service directly over cluster DNS — sends NO Kubernetes credentials
	// to the workload. Host and port come from the cache object, not the request.
	target := fmt.Sprintf("%s://%s.%s.svc.cluster.local:%d%s", scheme, svc.Name, svc.Namespace, matchedPort, path)

	ctx, cancel := context.WithTimeout(r.Context(), curlTimeout)
	defer cancel()

	preq, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid target")
		return
	}

	start := time.Now()
	resp, err := curlDialClient().Do(preq)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, friendlyDialError(err))
		return
	}
	defer resp.Body.Close()

	// Read one byte past the cap so we can flag truncation without loading a huge body.
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, curlMaxBodyBytes+1))
	truncated := len(body) > curlMaxBodyBytes
	if truncated {
		body = body[:curlMaxBodyBytes]
	}
	dur := time.Since(start).Milliseconds()

	// A read error after headers arrived means the target stalled or reset
	// mid-body. Surface it rather than passing off a partial body as a clean
	// response — diagnosing that stall is the whole point of the curl.
	var curlErr string
	if readErr != nil {
		if ctx.Err() != nil {
			curlErr = fmt.Sprintf("response timed out after %s (headers received, body incomplete)", curlTimeout)
		} else {
			curlErr = fmt.Sprintf("error reading response body: %v", readErr)
		}
	}

	headers := make(map[string]string, len(resp.Header))
	for k := range resp.Header {
		headers[k] = resp.Header.Get(k)
	}

	s.writeJSON(w, curlResponse{
		Status:     resp.StatusCode,
		StatusText: http.StatusText(resp.StatusCode),
		DurationMs: dur,
		Headers:    headers,
		Body:       string(body),
		Truncated:  truncated,
		BodyBytes:  len(body),
		Error:      curlErr,
	})
}
