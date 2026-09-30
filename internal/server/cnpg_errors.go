package server

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// PostgreSQL conditions Radar names from a relayed SQLSTATE: the ones an
// operator can act on. Other codes add nothing.
var cnpgSQLStateNames = map[string]string{
	"53000": "insufficient_resources",
	"53100": "disk_full",
	"53200": "out_of_memory",
	"53300": "too_many_connections",
	"53400": "configuration_limit_exceeded",
	"57014": "query_canceled",
	"57P01": "admin_shutdown",
	"57P02": "crash_shutdown",
	"57P03": "cannot_connect_now",
	"57P04": "database_dropped",
	"08000": "connection_exception",
	"08001": "sqlclient_unable_to_establish_sqlconnection",
	"08003": "connection_does_not_exist",
	"08004": "sqlserver_rejected_establishment_of_sqlconnection",
	"08006": "connection_failure",
	"28000": "invalid_authorization_specification",
	"28P01": "invalid_password",
	"3D000": "invalid_catalog_name",
	"42501": "insufficient_privilege",
	"25006": "read_only_sql_transaction",
	"40001": "serialization_failure",
	"40P01": "deadlock_detected",
	"55000": "object_not_in_prerequisite_state",
	"55006": "object_in_use",
	"55P03": "lock_not_available",
	"XX000": "internal_error",
	"XX001": "data_corrupted",
	"XX002": "index_corrupted",
}

// PostgreSQL messages Radar recognizes, each replaced by a fixed phrase.
// Matching text is never echoed: it can sit next to user names, hosts and
// connection strings.
var cnpgPostgresPhrases = []struct{ match, phrase string }{
	{"out of shared memory", "out of shared memory"},
	{"out of memory", "out of memory"},
	{"too many clients already", "too many clients already"},
	{"remaining connection slots are reserved", "remaining connection slots are reserved"},
	{"the database system is starting up", "the database system is starting up"},
	{"the database system is shutting down", "the database system is shutting down"},
	{"the database system is in recovery mode", "the database system is in recovery mode"},
	{"no space left on device", "no space left on device"},
	{"could not extend file", "could not extend a data file"},
	{"password authentication failed", "password authentication failed"},
}

var cnpgSQLStateRe = regexp.MustCompile(`SQLSTATE[ :]*([0-9A-Z]{5})\b`)

// cnpgPostgresDetail names what PostgreSQL reported inside a relayed error,
// from an allowlist only: a fixed phrase for a recognized message and the
// condition name of a recognized SQLSTATE. It never returns the error's own
// text, so nothing it carries (credentials, hosts, user names) reaches the
// UI. "" when neither is recognized.
func cnpgPostgresDetail(raw string) string {
	lower := strings.ToLower(raw)
	phrase := ""
	for _, p := range cnpgPostgresPhrases {
		if strings.Contains(lower, p.match) {
			phrase = p.phrase
			break
		}
	}
	condition := ""
	if m := cnpgSQLStateRe.FindStringSubmatch(raw); m != nil {
		if name, ok := cnpgSQLStateNames[m[1]]; ok {
			condition = name + " (SQLSTATE " + m[1] + ")"
		}
	}
	switch {
	case phrase != "" && condition != "":
		return phrase + ", " + condition
	case phrase != "":
		return phrase
	}
	return condition
}

// Prometheus client_golang's error page, which the exporters serve on a
// failed scrape; nothing in front of the Pod writes it.
const cnpgPromHTTPErrorPrefix = "An error has occurred while serving metrics"

// cnpgRelayedPodSentence says in plain words what a 5xx relayed through
// pods/proxy means. A relayed body proves nothing about where it came from:
// a gateway or the apiserver's own timeout page carries the same shape. So
// the instance manager or exporter is named only when the body says so —
// a PostgreSQL failure Radar recognizes on the status port, or client_golang's
// error page on a metrics port. Anything else is worded without an origin.
// The body can name sockets and connection strings; callers log it instead.
// ok is false for errors that are not a relayed 5xx.
func cnpgRelayedPodSentence(err error, port int) (string, bool) {
	body, code, ok := cnpgRelayedPodBody(err)
	if !ok || code < 500 {
		return "", false
	}
	if strings.Contains(strings.ToLower(body), "error trying to reach service") {
		return "", false
	}
	if port == cnpgStatusPort {
		if plain, ok := cnpgPostgresSentence(body); ok {
			return plain, true
		}
		if detail := cnpgPostgresDetail(body); detail != "" {
			return "the instance manager could not read PostgreSQL's status: " + detail, true
		}
	} else if strings.HasPrefix(strings.TrimSpace(body), cnpgPromHTTPErrorPrefix) {
		return fmt.Sprintf("the metrics endpoint on port %d answered with an error (HTTP %d)", port, code), true
	}
	return fmt.Sprintf("the read through the Kubernetes API failed with HTTP %d (from a gateway or the Pod; Radar can't tell which)", code), true
}

// cnpgRelayedPodBody is the body of an answer the apiserver relayed from the
// Pod through pods/proxy, with its HTTP code. client-go marks a response that
// is not an apiserver Status (the Pod's own answer) with an
// UnexpectedServerResponse cause carrying the body; errors the apiserver
// produced itself (Timeout, ServiceUnavailable, InternalError) carry none.
func cnpgRelayedPodBody(err error) (string, int, bool) {
	var status apierrors.APIStatus
	if err == nil || !errors.As(err, &status) {
		return "", 0, false
	}
	st := status.Status()
	if st.Details == nil {
		return "", 0, false
	}
	for _, c := range st.Details.Causes {
		if c.Type == metav1.CauseTypeUnexpectedServerResponse {
			return c.Message, int(st.Code), true
		}
	}
	return "", 0, false
}
