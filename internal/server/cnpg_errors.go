package server

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// Transport failures between Radar and the Kubernetes API, as client-go words
// them. None of these can come back from the Pod through the proxy.
var cnpgAPITransportHints = []string{
	"i/o timeout",
	"eof",
	"connection reset",
	"connection refused",
	"tls handshake timeout",
	"no such host",
	"broken pipe",
	"use of closed network connection",
	"http2: client connection lost",
	"timeout awaiting response headers",
	"client.timeout exceeded",
}

// cnpgTransportSentence says in plain words why a read through the Kubernetes
// API failed in transit. The raw error carries the API server's address and
// proxy URL, which mean nothing to the reader; callers log it instead. ok is
// false for any other error, whose own text the caller keeps. port is the Pod
// port that was being read (0 when none), timeout the caller's deadline.
func cnpgTransportSentence(err error, port int, timeout time.Duration) (string, bool) {
	if err == nil {
		return "", false
	}
	lower := strings.ToLower(err.Error())
	on := ""
	if port > 0 {
		on = fmt.Sprintf(" on port %d", port)
	}
	var status apierrors.APIStatus
	// The API server answered and relayed a failure to reach the Pod.
	relayed := (errors.As(err, &status) && status.Status().Code != 0) || strings.Contains(lower, "error trying to reach service")
	switch {
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(lower, "context deadline exceeded"):
		return fmt.Sprintf("the Pod did not answer%s within %s", on, cnpgSeconds(timeout)), true
	case strings.Contains(lower, "container not found"):
		return "the container is not running", true
	case relayed && strings.Contains(lower, "connection refused"):
		if port > 0 {
			return fmt.Sprintf("nothing is listening on port %d in the Pod", port), true
		}
		return "the Pod refused the connection", true
	case relayed && (strings.Contains(lower, "no route to host") || strings.Contains(lower, "host is unreachable") || strings.Contains(lower, "network is unreachable")):
		return "the Kubernetes API cannot reach the Pod's address", true
	case relayed && strings.Contains(lower, "timeout"):
		return "the Pod did not answer" + on, true
	case relayed:
		return "", false
	}
	for _, hint := range cnpgAPITransportHints {
		if strings.Contains(lower, hint) {
			return "the Kubernetes API did not answer", true
		}
	}
	return "", false
}

func cnpgSeconds(d time.Duration) string {
	return fmt.Sprintf("%g s", d.Seconds())
}

// cnpgPostgresSentence says in plain words why the instance manager or psql
// could not talk to PostgreSQL, or ok false when the text is not one it
// recognizes. A missing or refusing local socket means the server is down.
func cnpgPostgresSentence(raw string) (string, bool) {
	lower := strings.ToLower(raw)
	connecting := strings.Contains(lower, ".s.pgsql.") || strings.Contains(lower, "dial unix") || strings.Contains(lower, "failed to connect")
	down := strings.Contains(lower, "no such file or directory") || strings.Contains(lower, "connection refused")
	switch {
	case strings.Contains(lower, "the database system is starting up"):
		return "PostgreSQL is starting up on this instance", true
	case strings.Contains(lower, "the database system is shutting down"):
		return "PostgreSQL is shutting down on this instance", true
	case connecting && down:
		return "PostgreSQL is not running on this instance", true
	}
	return "", false
}

// Where PostgreSQL's own message starts in a relayed error: lib/pq's prefix,
// or the server's severity.
var cnpgPostgresMessageMarkers = []string{"pq: ", "ERROR: ", "FATAL: ", "PANIC: "}

var (
	cnpgConnParamRe = regexp.MustCompile(`\b(user|database|dbname|host|hostaddr|port|password|sslmode|application_name)=\S+`)
	cnpgSocketRe    = regexp.MustCompile(`\S*\.s\.PGSQL\.\d+\S*`)
	cnpgAddressRe   = regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}(:\d+)?\b`)
	cnpgSpacesRe    = regexp.MustCompile(`\s+`)
)

// cnpgPostgresDetail is PostgreSQL's own message inside a relayed error, e.g.
// "out of shared memory", with connection parameters, socket paths and
// addresses removed, or "" when there is none. It keeps an unrecognized
// failure diagnosable in the product without echoing how Radar connects.
func cnpgPostgresDetail(raw string) string {
	body := raw
	if i := strings.Index(body, `("`); i >= 0 {
		if j := strings.LastIndex(body, `") has prevented`); j > i {
			body = body[i+2 : j]
		}
	}
	body = strings.ReplaceAll(body, `\"`, `"`)
	start := -1
	for _, m := range cnpgPostgresMessageMarkers {
		if i := strings.Index(body, m); i >= 0 && (start < 0 || i < start) {
			start = i + len(m)
		}
	}
	if start < 0 {
		return ""
	}
	d := body[start:]
	d = cnpgConnParamRe.ReplaceAllString(d, "")
	d = cnpgSocketRe.ReplaceAllString(d, "the local socket")
	d = cnpgAddressRe.ReplaceAllString(d, "an address")
	d = strings.Trim(cnpgSpacesRe.ReplaceAllString(d, " "), " :;,")
	if len(d) > 160 {
		d = d[:160] + "…"
	}
	return d
}

// cnpgRelayedPodSentence says in plain words what an error answer from the
// Pod itself means, once the apiserver relays it as a 5xx. The body is the
// instance manager's or exporter's own error text, which names sockets and
// connection strings; callers log it instead. ok is false for anything the
// apiserver produced itself (a failure to reach the Pod) and for non-5xx.
func cnpgRelayedPodSentence(err error, port int) (string, bool) {
	var status apierrors.APIStatus
	if err == nil || !errors.As(err, &status) || status.Status().Code < 500 {
		return "", false
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "error trying to reach service") {
		return "", false
	}
	if port == cnpgStatusPort {
		if plain, ok := cnpgPostgresSentence(lower); ok {
			return plain, true
		}
		if detail := cnpgPostgresDetail(err.Error()); detail != "" {
			return "the instance manager could not read PostgreSQL's status: " + detail, true
		}
		return "the instance manager could not read PostgreSQL's status", true
	}
	return fmt.Sprintf("the metrics endpoint on port %d answered with an error (HTTP %d)", port, status.Status().Code), true
}
