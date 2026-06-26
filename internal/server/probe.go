package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/auth"
)

const (
	probeTimeout      = 10 * time.Second
	probeMaxBodyBytes = 512 * 1024 // cap the previewed response body at 512 KiB
)

// dnsName matches a DNS-1123 subdomain (namespace/service names). Port accepts a
// numeric port or a DNS-1123 port name. Both are validated before being spliced
// into the apiserver proxy URL so a crafted value can't escape the
// services/{target}/proxy path structure.
var (
	probeNameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)
	probePortRe = regexp.MustCompile(`^[a-zA-Z0-9]([-a-zA-Z0-9]{0,14})?$`)
)

type probeRequest struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`   // Service name
	Port      string `json:"port"`   // port number or named port
	Scheme    string `json:"scheme"` // "http" (default) or "https"
	Path      string `json:"path"`   // request path, defaults to "/"
}

type probeResponse struct {
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

// probeHTTPClient builds the client used for a probe. Critically it disables
// redirect-following: the client-go transport re-attaches the cluster bearer
// token and Impersonate-* headers to every request it makes, so following a
// Service-controlled 3xx to another host would leak those credentials to an
// attacker-chosen endpoint (SSRF). Instead we surface the 3xx + Location to the
// caller as the probe result.
func probeHTTPClient(cfg *rest.Config) (*http.Client, error) {
	client, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return nil, err
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return client, nil
}

// proxyStatusError recognizes a Kubernetes Status object — what the apiserver's
// services/proxy returns when it can't deliver the request to a backend (no ready
// endpoints, connection refused) — and maps it to a plain-English message. The
// Service never saw the request in these cases, so the raw Status JSON shouldn't
// be shown as if it were the Service's response. Returns ok=false for a normal
// app response (apps don't emit kind:"Status" objects).
func proxyStatusError(body []byte) (string, bool) {
	var st struct {
		Kind    string `json:"kind"`
		Status  string `json:"status"`
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &st) != nil || st.Kind != "Status" || st.Status != "Failure" {
		return "", false
	}
	switch {
	case strings.Contains(st.Message, "no endpoints available"):
		return "No ready endpoints — this Service has no running pods behind it.", true
	case st.Message != "":
		return "The cluster could not reach this Service: " + st.Message, true
	default:
		return "The cluster could not reach this Service.", true
	}
}

// handleProbeService issues a single server-side GET to an in-cluster Service via
// the apiserver's services/proxy subresource, impersonating the caller so the
// apiserver enforces RBAC (get services/proxy). This is the Cloud-safe stand-in
// for port-forward when the question is "what does this endpoint return": the
// request originates in-cluster and the response flows back to the browser — no
// local listener, and the target is constrained to the named Service (not
// arbitrary egress).
func (s *Server) handleProbeService(w http.ResponseWriter, r *http.Request) {
	var req probeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	req.Namespace = strings.TrimSpace(req.Namespace)
	req.Name = strings.TrimSpace(req.Name)
	req.Port = strings.TrimSpace(req.Port)
	if !probeNameRe.MatchString(req.Namespace) {
		s.writeError(w, http.StatusBadRequest, "invalid namespace")
		return
	}
	if !probeNameRe.MatchString(req.Name) {
		s.writeError(w, http.StatusBadRequest, "invalid service name")
		return
	}
	if !probePortRe.MatchString(req.Port) {
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

	// The proxy suffix is the path the backend Service sees. Keep it as a rooted,
	// single-line path; the apiserver only proxies it to the target so the blast
	// radius stays within the (RBAC-gated) Service.
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

	cfg := s.getConfigForRequest(r)
	if cfg == nil {
		s.writeError(w, http.StatusServiceUnavailable, "K8s client not initialized")
		return
	}
	auth.AuditLog(r, req.Namespace, req.Name)

	httpClient, err := probeHTTPClient(cfg)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "failed to build cluster client")
		return
	}

	// services/proxy target encodes [scheme:]name:port; the apiserver authorizes
	// "get services/proxy" for the impersonated user before reaching the Service.
	target := fmt.Sprintf("%s:%s:%s", scheme, req.Name, req.Port)
	proxyURL := strings.TrimRight(cfg.Host, "/") +
		"/api/v1/namespaces/" + req.Namespace + "/services/" + target + "/proxy" + path

	ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
	defer cancel()

	preq, err := http.NewRequestWithContext(ctx, http.MethodGet, proxyURL, nil)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid target")
		return
	}

	start := time.Now()
	resp, err := httpClient.Do(preq)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, fmt.Sprintf("probe failed: %v", err))
		return
	}
	defer resp.Body.Close()

	// Read one byte past the cap so we can flag truncation without loading a huge body.
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, probeMaxBodyBytes+1))
	truncated := len(body) > probeMaxBodyBytes
	if truncated {
		body = body[:probeMaxBodyBytes]
	}
	dur := time.Since(start).Milliseconds()

	// A read error after headers arrived means the target stalled or reset
	// mid-body. Surface it rather than passing off a partial body as a clean
	// response — diagnosing that stall is the whole point of the probe.
	var probeErr string
	if readErr != nil {
		if ctx.Err() != nil {
			probeErr = fmt.Sprintf("response timed out after %s (headers received, body incomplete)", probeTimeout)
		} else {
			probeErr = fmt.Sprintf("error reading response body: %v", readErr)
		}
	}

	respBody := string(body)
	// A failure that the Service itself never produced — the apiserver proxy
	// couldn't deliver the request (no ready endpoints, connection refused) and
	// returned a Kubernetes Status object. Surface that as plain English instead
	// of dumping a raw k8s API error at the operator, and drop the body since it
	// isn't the Service's response.
	if probeErr == "" {
		if friendly, ok := proxyStatusError(body); ok {
			probeErr = friendly
			respBody = ""
		}
	}

	headers := make(map[string]string, len(resp.Header))
	for k := range resp.Header {
		headers[k] = resp.Header.Get(k)
	}

	s.writeJSON(w, probeResponse{
		Status:     resp.StatusCode,
		StatusText: http.StatusText(resp.StatusCode),
		DurationMs: dur,
		Headers:    headers,
		Body:       respBody,
		Truncated:  truncated,
		BodyBytes:  len(body),
		Error:      probeErr,
	})
}
