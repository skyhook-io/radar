package cnpg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// The only requests the runtime endpoints can make: a GET of these fixed
// paths on these fixed ports of a validated Pod. The instance manager's port
// also serves endpoints that change state (some on GET), so the fixed path and
// the refusal to follow redirects are the security boundary. Nothing of the
// caller's request reaches the proxied URL or its headers.
const (
	cnpgStatusPort    = 8000
	cnpgMetricsPort   = 9187
	poolerMetricsPort = 9127
	cnpgStatusPath    = "/pg/status"
	metricsPath       = "/metrics"

	cnpgRuntimeRequestTimeout = 5 * time.Second
	cnpgRuntimeConcurrency    = 4
	cnpgRuntimeMaxRows        = 200

	runtimeStateOK          = "ok"
	cnpgRuntimeStatePartial = "partial"
	runtimeStateDenied      = "denied"
	runtimeStateUnreachable = "unreachable"
	runtimeStateError       = "error"

	cnpgPoolerNameLabel      = "cnpg.io/poolerName"
	pgBouncerContainer       = "pgbouncer"
	cnpgStatusPortTLSFlag    = "--status-port-tls"
	metricsPortTLSFlag       = "--metrics-port-tls"
	cnpgMetricsExporterApp   = "cnpg_metrics_exporter"
	cnpgPgBouncerAdminDB     = "pgbouncer"
	cnpgPgBouncerAuthUser    = "cnpg_pooler_pgbouncer"
	cnpgFencedErrorExplained = "instance is fenced, which asks the operator to stop PostgreSQL"
)

// Raw-size caps, enforced before parsing, and memo lifetimes sized to the
// frontend's polling (status ~5s, metrics ~30s). Variables so tests can shrink
// them.
var (
	cnpgRuntimeStatusCap int64 = 1 << 20
	runtimeMetricsCap    int64 = 4 << 20
	cnpgStatusMemoTTL          = 5 * time.Second
	metricsMemoTTL             = 25 * time.Second
)

// cnpgContainerHasFlag reports whether the running Pod's container was
// started with a flag — how the operator turns TLS on for a port.
func containerHasFlag(p *corev1.Pod, container, flag string) bool {
	for _, c := range p.Spec.Containers {
		if c.Name != container {
			continue
		}
		for _, words := range [][]string{c.Command, c.Args} {
			for _, w := range words {
				if w == flag {
					return true
				}
			}
		}
	}
	return false
}

func schemeFor(tls bool) string {
	if tls {
		return "https"
	}
	return "http"
}

type proxyTarget struct {
	namespace, pod string
	podUID         types.UID
	port           int
	path           string
	scheme         string
	limit          int64
}

type cnpgProxyOutcome struct {
	state      string
	err        string
	scheme     string
	capturedAt string
	body       []byte
	// truncated: the answer exceeded the cap and body holds only its first
	// limit bytes.
	truncated bool
	// schemeMismatch: the failure was the wrong protocol on the port, the only
	// failure that justifies trying the other scheme.
	schemeMismatch bool
}

func (o cnpgProxyOutcome) source() CNPGRuntimeSource {
	return CNPGRuntimeSource{State: o.state, Error: o.err, Scheme: o.scheme, CapturedAt: o.capturedAt}
}

// cnpgProxyGetWithFallback tries the declared scheme and, only when that
// failed as a protocol mismatch, the other one once: a Pod created by an older
// operator may disagree with what its object declares today.
func proxyGetWithFallback(ctx context.Context, client kubernetes.Interface, t proxyTarget) cnpgProxyOutcome {
	first := cnpgProxyGet(ctx, client, t, t.scheme)
	if !first.schemeMismatch {
		return first
	}
	other := "https"
	if t.scheme == "https" {
		other = "http"
	}
	second := cnpgProxyGet(ctx, client, t, other)
	if second.schemeMismatch {
		second.state = runtimeStateUnreachable
		second.err = fmt.Sprintf("neither http nor https worked on port %d: %s", t.port, first.err)
		second.scheme = ""
	}
	return second
}

// cnpgProxyGet issues one GET through the apiserver's pods/proxy, built only
// from the validated Pod and the fixed port and path.
func cnpgProxyGet(ctx context.Context, client kubernetes.Interface, t proxyTarget, scheme string) cnpgProxyOutcome {
	ctx, cancel := context.WithTimeout(ctx, cnpgRuntimeRequestTimeout)
	defer cancel()
	out := cnpgProxyOutcome{scheme: scheme, capturedAt: time.Now().UTC().Format(time.RFC3339)}
	stream, err := client.CoreV1().RESTClient().Get().
		Namespace(t.namespace).
		Resource("pods").
		Name(fmt.Sprintf("%s:%s:%d", scheme, t.pod, t.port)).
		SubResource("proxy").
		Suffix(t.path).
		Stream(ctx)
	if err != nil {
		return classifyCNPGProxyFailure(ctx, err, out, t)
	}
	defer stream.Close()
	body, err := io.ReadAll(io.LimitReader(stream, t.limit+1))
	if err != nil {
		return classifyCNPGProxyFailure(ctx, err, out, t)
	}
	if int64(len(body)) > t.limit {
		out.body, out.truncated = body[:t.limit], true
	} else {
		out.body = body
	}
	out.state = runtimeStateOK
	return out
}

// What the apiserver relays when the scheme is wrong, in either direction.
// Certificate-verification failures are deliberately absent: those are a
// verdict on the TLS setup, not a sign the port speaks plain HTTP.
var cnpgSchemeMismatchHints = []string{
	"http: server gave http response to https client",
	"client sent an http request to an https server",
	"first record does not look like a tls handshake",
	"malformed http response",
}

// cnpgRelayedTransportTimeout: the apiserver's proxy reports its own dial or
// read timeout to the Pod as a 503 whose message is "error trying to reach
// service: <transport error>". Only the transport error's end is matched, so a
// name or URL containing "timeout" never counts.
func cnpgRelayedTransportTimeout(err error) bool {
	if !apierrors.IsServiceUnavailable(err) {
		return false
	}
	var status apierrors.APIStatus
	if !errors.As(err, &status) {
		return false
	}
	msg := status.Status().Message
	return strings.HasPrefix(msg, "error trying to reach service:") &&
		(strings.HasSuffix(msg, ": i/o timeout") || strings.HasSuffix(msg, ": context deadline exceeded"))
}

func classifyCNPGProxyFailure(ctx context.Context, err error, out cnpgProxyOutcome, t proxyTarget) cnpgProxyOutcome {
	cnpgMarkTimedOut(ctx, err)
	out = classifyCNPGProxyError(ctx, err, out)
	if out.state == runtimeStateDenied || errors.Is(err, context.Canceled) {
		return out
	}
	cause := err
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		cause = context.DeadlineExceeded
	}
	// The Pod's own error answer can carry "connection refused" from
	// PostgreSQL's socket, which is not a transport failure; read it first.
	if plain, ok := cnpgRelayedPodSentence(err, t.port); ok && !out.schemeMismatch {
		log.Printf("[cnpg] %s/%s port %d %s answered with an error: %v", t.namespace, t.pod, t.port, t.path, err)
		out.err = plain
	} else if plain, ok := cnpgTransportSentence(cause, t.port, cnpgRuntimeRequestTimeout); ok {
		log.Printf("[cnpg] Failed to read %s/%s port %d %s: %v", t.namespace, t.pod, t.port, t.path, err)
		out.err = plain
	}
	return out
}

func classifyCNPGProxyError(ctx context.Context, err error, out cnpgProxyOutcome) cnpgProxyOutcome {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		out.state, out.err = runtimeStateUnreachable, fmt.Sprintf("no answer within %s", cnpgRuntimeRequestTimeout)
		return out
	}
	if errors.Is(err, context.Canceled) {
		out.state, out.err = runtimeStateError, "request cancelled"
		return out
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	// The apiserver's own refusal names the pods/proxy subresource, which an
	// answer relayed from the Pod never does.
	if apierrors.IsForbidden(err) && strings.Contains(lower, "proxy") {
		out.state, out.err = runtimeStateDenied, "the apiserver denied get pods/proxy"
		return out
	}
	code := 0
	var status apierrors.APIStatus
	if errors.As(err, &status) {
		code = int(status.Status().Code)
	}
	out.err = truncateCNPGRuntimeError(msg)
	certificate := strings.Contains(lower, "x509") || strings.Contains(lower, "certificate")
	if !certificate {
		// A plain request against a TLS port comes back as a bare 400: nothing
		// else makes a GET of these fixed paths a bad request.
		out.schemeMismatch = code == http.StatusBadRequest
		for _, hint := range cnpgSchemeMismatchHints {
			if strings.Contains(lower, hint) {
				out.schemeMismatch = true
			}
		}
	}
	switch {
	case code >= 300 && code < 400:
		out.state, out.err = runtimeStateError, fmt.Sprintf("the Pod answered with a redirect (%d), which is not followed", code)
		out.schemeMismatch = false
	case out.schemeMismatch, code == 0, code >= 500:
		out.state = runtimeStateUnreachable
	default:
		out.state = runtimeStateError
	}
	return out
}

func truncateCNPGRuntimeError(s string) string {
	const limit = 300
	if len(s) > limit {
		return s[:limit] + "…"
	}
	return s
}

func formatCNPGByteCap(n int64) string {
	if n >= 1<<20 && n%(1<<20) == 0 {
		return fmt.Sprintf("%d MiB", n>>20)
	}
	return fmt.Sprintf("%d bytes", n)
}
