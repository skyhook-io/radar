package k8score

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultRequestDeadline bounds one non-streaming GET (a single object, or
	// one page of a LIST). It is twice the apiserver's default
	// --request-timeout for non-long-running requests, so a LIST the server
	// is still allowed to finish is not cut short on an ordinary setup.
	DefaultRequestDeadline = 2 * time.Minute
	// DefaultWatchDeadlineGrace is added to a watch's timeoutSeconds. The
	// apiserver ends such a watch itself after timeoutSeconds, so the client
	// deadline only fires when the connection died without a reset — for
	// example a proxy whose upstream dropped silently.
	DefaultWatchDeadlineGrace = time.Minute
)

// WrapRequestDeadlines returns a RoundTripper that gives informer-style GETs a
// client-side deadline, for use as a rest.Config WrapTransport.
//
// rest.Config.Timeout cannot do this job: it becomes http.Client.Timeout and
// would also end every long-lived watch, log stream and exec session. Without
// any deadline, a LIST whose connection stalls without a TCP reset blocks the
// reflector forever and its store silently stops updating.
//
// Each HTTP attempt gets its own deadline; client-go may retry an attempt.
// Exempt: non-GET requests, protocol upgrades (exec/attach/port-forward),
// follow=true log streams, the apiserver's long-running subresources (log,
// exec, attach, portforward, proxy), and watches without timeoutSeconds.
func WrapRequestDeadlines(rt http.RoundTripper) http.RoundTripper {
	return &requestDeadlineRoundTripper{
		next:       rt,
		deadline:   DefaultRequestDeadline,
		watchGrace: DefaultWatchDeadlineGrace,
	}
}

type requestDeadlineRoundTripper struct {
	next       http.RoundTripper
	deadline   time.Duration
	watchGrace time.Duration
}

func (t *requestDeadlineRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	d := t.deadlineFor(req)
	if d <= 0 {
		return t.next.RoundTrip(req)
	}
	ctx, cancel := context.WithTimeout(req.Context(), d)
	resp, err := t.next.RoundTrip(req.WithContext(ctx))
	if err != nil {
		cancel()
		return resp, err
	}
	resp.Body = &cancelOnCloseBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// deadlineFor returns the deadline for req, or 0 when it must stay unbounded.
func (t *requestDeadlineRoundTripper) deadlineFor(req *http.Request) time.Duration {
	if req.Method != http.MethodGet || req.Header.Get("Upgrade") != "" {
		return 0
	}
	q := req.URL.Query()
	if q.Get("follow") == "true" {
		return 0
	}
	subresource, legacyWatch := apiPathSubresource(req.URL.Path)
	if longRunningSubresources[subresource] {
		return 0
	}
	if w := q.Get("watch"); w == "true" || w == "1" || legacyWatch {
		secs, err := strconv.ParseInt(q.Get("timeoutSeconds"), 10, 64)
		if err != nil || secs <= 0 {
			return 0
		}
		return time.Duration(secs)*time.Second + t.watchGrace
	}
	return t.deadline
}

// longRunningSubresources are the subresources the apiserver itself treats as
// long-running (no server-side request timeout).
var longRunningSubresources = map[string]bool{
	"log":         true,
	"exec":        true,
	"attach":      true,
	"portforward": true,
	"proxy":       true,
}

type cancelOnCloseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnCloseBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// namespaceSubresources are subresources of a Namespace object, which share
// the /namespaces/{name}/{x} shape with namespaced collections.
var namespaceSubresources = map[string]bool{"status": true, "finalize": true}

// isAPIVersion matches Kubernetes API version names: v1, v2beta1, v1alpha3.
func isAPIVersion(s string) bool {
	if len(s) < 2 || s[0] != 'v' || s[1] < '1' || s[1] > '9' {
		return false
	}
	rest := strings.TrimLeft(s[1:], "0123456789")
	for _, suffix := range []string{"alpha", "beta"} {
		if strings.HasPrefix(rest, suffix) {
			n := strings.TrimPrefix(rest, suffix)
			return n != "" && strings.Trim(n, "0123456789") == ""
		}
	}
	return rest == ""
}

// apiPathSubresource reads an apiserver path the way the apiserver's own
// RequestInfoFactory does — /api/{v}/... or /apis/{group}/{v}/..., an
// optional legacy watch/ prefix, an optional namespaces/{ns}/, then
// resource/name/subresource — and returns the subresource and whether the
// legacy watch prefix was used. Matching by position means a resource or
// object that happens to be named "log" or "proxy" is not mistaken for the
// subresource. A prefix before the API root (a proxied server URL such as
// /k8s/clusters/x/api/v1) is skipped: the root is the first api/{version} or
// apis/{group}/{version}, so a prefix segment named "api" is not mistaken for it.
func apiPathSubresource(path string) (subresource string, legacyWatch bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	start := -1
	for i, s := range parts {
		if s == "api" && i+1 < len(parts) && isAPIVersion(parts[i+1]) {
			start = i + 2
			break
		}
		if s == "apis" && i+2 < len(parts) && isAPIVersion(parts[i+2]) {
			start = i + 3
			break
		}
	}
	if start < 0 || start >= len(parts) {
		return "", false
	}
	parts = parts[start:]
	if parts[0] == "watch" {
		legacyWatch = true
		parts = parts[1:]
	}
	if len(parts) > 2 && parts[0] == "namespaces" && !namespaceSubresources[parts[2]] {
		parts = parts[2:]
	}
	if len(parts) > 2 {
		subresource = parts[2]
	}
	return subresource, legacyWatch
}
