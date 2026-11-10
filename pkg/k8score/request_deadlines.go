package k8score

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// requestIdleTimeout ends a read when no response bytes have
	// arrived for this long. A transfer that keeps delivering bytes, however
	// slowly, is never cut.
	requestIdleTimeout = time.Minute
	// responseHeaderTimeout bounds the wait for response headers: the
	// apiserver can take a while to start answering a large LIST.
	responseHeaderTimeout = 3 * time.Minute
	// requestCeiling bounds a whole non-watch read, only to stop a
	// request that keeps trickling bytes forever.
	requestCeiling = 15 * time.Minute
	// watchDeadlineGrace is added to a watch's timeoutSeconds. The
	// apiserver ends such a watch itself after timeoutSeconds, so the client
	// deadline only fires when the connection died without a reset — for
	// example a proxy whose upstream dropped silently.
	watchDeadlineGrace = time.Minute
)

// WrapRequestDeadlines returns a rest.Config WrapTransport that ends
// informer-style GETs whose connection stalls. serverPath is the path part of
// the config's Host (for example "/k8s/clusters/c-1" behind a proxy, usually
// empty); it is stripped before a request path is classified, so a prefix that
// looks like an API path can't change the classification.
//
// rest.Config.Timeout cannot do this job: it becomes http.Client.Timeout and
// would also end every long-lived watch, log stream and exec session. Without
// any bound, a LIST whose connection stalls without a TCP reset blocks the
// reflector forever and its store silently stops updating.
//
// A non-watch GET ends when its headers take longer than
// responseHeaderTimeout, when no body bytes arrive for
// requestIdleTimeout, or after requestCeiling in total. A watch
// with timeoutSeconds ends at timeoutSeconds plus watchDeadlineGrace;
// it is never idle-timed, because a healthy watch can go quiet. Each HTTP
// attempt gets its own bounds; client-go may retry an attempt. Exempt:
// non-GET requests, protocol upgrades (exec/attach/port-forward), follow=true
// log streams, the apiserver's long-running subresources (log, exec, attach,
// portforward, proxy), and watches without timeoutSeconds.
func WrapRequestDeadlines(serverPath string) func(http.RoundTripper) http.RoundTripper {
	serverPath = strings.TrimRight(serverPath, "/")
	return func(rt http.RoundTripper) http.RoundTripper {
		return &requestDeadlineRoundTripper{
			next:          rt,
			serverPath:    serverPath,
			idle:          requestIdleTimeout,
			headerTimeout: responseHeaderTimeout,
			ceiling:       requestCeiling,
			watchGrace:    watchDeadlineGrace,
		}
	}
}

type requestDeadlineRoundTripper struct {
	next          http.RoundTripper
	serverPath    string
	idle          time.Duration
	headerTimeout time.Duration
	ceiling       time.Duration
	watchGrace    time.Duration
}

// errRequestEnded is the cancellation cause, and the error a caller sees,
// when one of these bounds ends a read. It is deliberately not a timeout or
// connection error: client-go retries those inside a single Watch call and
// can swallow the last one, while this error reaches the reflector, which
// then lists again.
var errRequestEnded = errors.New("request ended by Radar")

func (t *requestDeadlineRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	bounded, watchDeadline := t.classify(req)
	switch {
	case !bounded:
		return t.next.RoundTrip(req)
	case watchDeadline > 0:
		ctx, cancel := context.WithTimeoutCause(req.Context(), watchDeadline,
			fmt.Errorf("%w: watch still open %s after its timeoutSeconds", errRequestEnded, t.watchGrace))
		resp, err := t.next.RoundTrip(req.WithContext(ctx))
		if err != nil {
			err = endCause(ctx, err)
			cancel()
			return resp, err
		}
		resp.Body = &causeBody{ReadCloser: resp.Body, ctx: ctx, stop: cancel}
		return resp, nil
	}

	ceilingCtx, cancelCeiling := context.WithTimeoutCause(req.Context(), t.ceiling,
		fmt.Errorf("%w: still running after %s", errRequestEnded, t.ceiling))
	ctx, cancel := context.WithCancelCause(ceilingCtx)
	stop := func() {
		cancel(nil)
		cancelCeiling()
	}
	headerTimer := time.AfterFunc(t.headerTimeout, func() {
		cancel(fmt.Errorf("%w: no response headers within %s", errRequestEnded, t.headerTimeout))
	})
	resp, err := t.next.RoundTrip(req.WithContext(ctx))
	headerTimer.Stop()
	if err != nil {
		err = endCause(ctx, err)
		stop()
		return resp, err
	}
	body := &idleTimeoutBody{causeBody: causeBody{ReadCloser: resp.Body, ctx: ctx, stop: stop}, idle: t.idle}
	body.timer = time.AfterFunc(t.idle, func() {
		cancel(fmt.Errorf("%w: no data received for %s", errRequestEnded, t.idle))
	})
	resp.Body = body
	return resp, nil
}

// endCause replaces the context error the HTTP stack reports with the bound
// that caused it, so logs and diagnostics say why the read ended. The original
// error is kept as text only, so the result is not a timeout to client-go.
func endCause(ctx context.Context, err error) error {
	if errors.Is(err, errRequestEnded) {
		return err
	}
	if cause := context.Cause(ctx); errors.Is(cause, errRequestEnded) {
		return fmt.Errorf("%w (%v)", cause, err)
	}
	return err
}

// classify reports whether req gets bounds at all and, for a watch, its total
// deadline (0 for every other bounded read).
func (t *requestDeadlineRoundTripper) classify(req *http.Request) (bounded bool, watchDeadline time.Duration) {
	if req.Method != http.MethodGet || req.Header.Get("Upgrade") != "" {
		return false, 0
	}
	q := req.URL.Query()
	if q.Get("follow") == "true" {
		return false, 0
	}
	subresource, legacyWatch := apiPathSubresource(strings.TrimPrefix(req.URL.Path, t.serverPath))
	if longRunningSubresources[subresource] {
		return false, 0
	}
	if w := q.Get("watch"); w == "true" || w == "1" || legacyWatch {
		secs, err := strconv.ParseInt(q.Get("timeoutSeconds"), 10, 64)
		if err != nil || secs <= 0 {
			return false, 0
		}
		return true, time.Duration(secs)*time.Second + t.watchGrace
	}
	return true, 0
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

// causeBody reports a read ended by one of these bounds with its cause, and
// releases the request's context on Close.
type causeBody struct {
	io.ReadCloser
	ctx  context.Context
	stop func()
}

func (b *causeBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && err != io.EOF {
		err = endCause(b.ctx, err)
	}
	return n, err
}

func (b *causeBody) Close() error {
	err := b.ReadCloser.Close()
	b.stop()
	return err
}

// idleTimeoutBody restarts the idle timer each time a Read delivers bytes, so
// only a body that stops delivering is cancelled. It measures the time between
// the caller's reads, so the caller must read the body steadily; client-go
// reads a response whole before decoding it.
type idleTimeoutBody struct {
	causeBody
	idle  time.Duration
	timer *time.Timer
}

func (b *idleTimeoutBody) Read(p []byte) (int, error) {
	n, err := b.causeBody.Read(p)
	if n > 0 {
		b.timer.Reset(b.idle)
	}
	return n, err
}

func (b *idleTimeoutBody) Close() error {
	b.timer.Stop()
	return b.causeBody.Close()
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
// subresource. path must already have the server's path prefix removed;
// anything that doesn't start at the API root has no subresource.
func apiPathSubresource(path string) (subresource string, legacyWatch bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	var start int
	switch {
	case len(parts) > 2 && parts[0] == "api" && isAPIVersion(parts[1]):
		start = 2
	case len(parts) > 3 && parts[0] == "apis" && isAPIVersion(parts[2]):
		start = 3
	default:
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
