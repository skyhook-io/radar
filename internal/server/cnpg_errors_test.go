package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestCNPGTransportSentence(t *testing.T) {
	relayed := apierrors.NewServiceUnavailable("error trying to reach service: dial tcp 10.0.0.5:9187: connect: connection refused")
	cases := []struct {
		err  error
		port int
		want string
	}{
		{context.DeadlineExceeded, 8000, "the Pod did not answer on port 8000 within 5 s"},
		{errors.New(`Get "https://127.0.0.1:55484/api/v1/namespaces/pg/pods/http:pg-1:9127/proxy/metrics": EOF`), 9127, "the Kubernetes API did not answer"},
		{errors.New("read tcp 127.0.0.1:56435->127.0.0.1:55484: i/o timeout"), 0, "the Kubernetes API did not answer"},
		{relayed, 9187, "nothing is listening on port 9187 in the Pod"},
		{fmt.Errorf("error trying to reach service: dial tcp 10.0.0.5:8000: connect: no route to host"), 8000, "the Kubernetes API cannot reach the Pod's address"},
	}
	for _, c := range cases {
		got, ok := cnpgTransportSentence(c.err, c.port, 5*time.Second)
		if !ok || got != c.want {
			t.Errorf("%v: %q (%v), want %q", c.err, got, ok, c.want)
		}
		if strings.Contains(got, "127.0.0.1") {
			t.Errorf("address leaked: %q", got)
		}
	}
	for _, err := range []error{
		errors.New("psql: error: FATAL: the database system is shutting down"),
		apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "pg-1", errors.New("denied")),
	} {
		if got, ok := cnpgTransportSentence(err, 0, 5*time.Second); ok {
			t.Errorf("%v is not a transport failure, got %q", err, got)
		}
	}
}

func TestCNPGRelayedPodSentence(t *testing.T) {
	socketGone := apierrors.NewGenericServerResponse(500, "get", schema.GroupResource{Resource: "pods"}, "https:pg-wal-failing-1:8000",
		"failed to connect to `user=postgres database=postgres`: /controller/run/.s.PGSQL.5432 (/controller/run): dial error: dial unix /controller/run/.s.PGSQL.5432: connect: no such file or directory", 0, true)
	other := apierrors.NewGenericServerResponse(500, "get", schema.GroupResource{Resource: "pods"}, "https:pg-1:8000", "pq: out of shared memory", 0, true)
	exporter := apierrors.NewGenericServerResponse(500, "get", schema.GroupResource{Resource: "pods"}, "http:pg-1:9187", "An error has occurred while serving metrics:\n\ncollector failed", 0, true)
	unknownExporter := apierrors.NewGenericServerResponse(503, "get", schema.GroupResource{Resource: "pods"}, "http:pg-1:9187", "collector failed", 0, true)
	gateway := apierrors.NewGenericServerResponse(504, "get", schema.GroupResource{Resource: "pods"}, "https:pg-1:8000", "<html><head><title>504 Gateway Time-out</title></head><body><center><h1>504 Gateway Time-out</h1></center><hr><center>nginx</center></body></html>", 0, true)
	untypedStatus := apierrors.NewGenericServerResponse(504, "get", schema.GroupResource{Resource: "pods"}, "https:pg-1:8000", `{"kind":"Status","status":"Failure","message":"Timeout: request did not complete within the allotted timeout","code":504}`, 0, true)
	opaque := apierrors.NewGenericServerResponse(500, "get", schema.GroupResource{Resource: "pods"}, "https:pg-1:8000", "internal server error", 0, true)
	neutral := func(code int) string {
		return fmt.Sprintf("the read through the Kubernetes API failed with HTTP %d (from a gateway or the Pod; Radar can't tell which)", code)
	}
	cases := []struct {
		err  error
		port int
		want string
	}{
		{socketGone, cnpgStatusPort, "PostgreSQL is not running on this instance"},
		{other, cnpgStatusPort, "the instance manager could not read PostgreSQL's status: out of shared memory"},
		{exporter, cnpgMetricsPort, "the metrics endpoint on port 9187 answered with an error (HTTP 500)"},
		{unknownExporter, cnpgMetricsPort, neutral(503)},
		{gateway, cnpgStatusPort, neutral(504)},
		{untypedStatus, cnpgStatusPort, neutral(504)},
		{opaque, cnpgStatusPort, neutral(500)},
	}
	for _, c := range cases {
		got, ok := cnpgRelayedPodSentence(c.err, c.port)
		if !ok || got != c.want {
			t.Errorf("%v: %q (%v), want %q", c.err, got, ok, c.want)
		}
		if strings.Contains(got, "controller/run") || strings.Contains(got, "pg-") {
			t.Errorf("raw text leaked: %q", got)
		}
	}
	for _, err := range []error{
		apierrors.NewServiceUnavailable("error trying to reach service: dial tcp 10.0.0.5:8000: connect: no route to host"),
		apierrors.NewBadRequest("bad"),
		errors.New("plain"),
	} {
		if got, ok := cnpgRelayedPodSentence(err, cnpgStatusPort); ok {
			t.Errorf("%v is not the Pod's own error answer, got %q", err, got)
		}
	}
}

func TestClassifyCNPGProxyFailureReplacesRelayedInstanceManagerError(t *testing.T) {
	err := apierrors.NewGenericServerResponse(500, "get", schema.GroupResource{Resource: "pods"}, "https:pg-1:8000",
		"failed to connect to `user=postgres database=postgres`: dial unix /controller/run/.s.PGSQL.5432: connect: no such file or directory", 0, true)
	out := classifyCNPGProxyFailure(context.Background(), err, cnpgProxyOutcome{}, cnpgProxyTarget{namespace: "pg", pod: "pg-1", port: cnpgStatusPort, path: cnpgStatusPath})
	if out.err != "PostgreSQL is not running on this instance" {
		t.Errorf("err = %q", out.err)
	}
}

func TestCNPGPostgresSentence(t *testing.T) {
	for raw, want := range map[string]string{
		`psql: error: connection to server on socket "/controller/run/.s.PGSQL.5432" failed: No such file or directory`: "PostgreSQL is not running on this instance",
		"FATAL: the database system is starting up": "PostgreSQL is starting up on this instance",
	} {
		if got, ok := cnpgPostgresSentence(raw); !ok || got != want {
			t.Errorf("%q: %q, want %q", raw, got, want)
		}
	}
	if got, ok := cnpgPostgresSentence("ERROR: permission denied for table x"); ok {
		t.Errorf("unrecognized text mapped to %q", got)
	}
}

func TestCNPGPostgresRefusalIsNotATransportFailure(t *testing.T) {
	relayed := apierrors.NewGenericServerResponse(500, "get", schema.GroupResource{Resource: "pods"}, "https:pg-1:8000",
		"failed to connect to `user=postgres database=postgres`: /controller/run/.s.PGSQL.5432 (/controller/run): dial error: dial unix /controller/run/.s.PGSQL.5432: connect: connection refused", 0, true)
	out := classifyCNPGProxyFailure(context.Background(), relayed, cnpgProxyOutcome{}, cnpgProxyTarget{namespace: "pg", pod: "pg-1", port: cnpgStatusPort, path: cnpgStatusPath})
	if out.err != "PostgreSQL is not running on this instance" {
		t.Errorf("relayed socket refusal = %q", out.err)
	}
	psql := errors.New(`command terminated with exit code 2: psql: error: connection to server on socket "/controller/run/.s.PGSQL.5432" failed: Connection refused`)
	if got := cnpgExecSourceState(psql); got.Error != "PostgreSQL is not running on this instance" || got.State != cnpgRuntimeStateError {
		t.Errorf("psql socket refusal = %+v", got)
	}
}

func TestCNPGPostgresDetailKeepsTheDiagnosisWithoutConnectionFacts(t *testing.T) {
	shm := apierrors.NewGenericServerResponse(500, "get", schema.GroupResource{Resource: "pods"}, "https:pg-1:8000", "while reading status: pq: out of shared memory", 0, true)
	if got, _ := cnpgRelayedPodSentence(shm, cnpgStatusPort); got != "the instance manager could not read PostgreSQL's status: out of shared memory" {
		t.Errorf("shared memory = %q", got)
	}
	slots := "query failed for user=postgres database=app host=10.0.0.5:5432: FATAL: remaining connection slots are reserved (SQLSTATE 53300)"
	if got := cnpgPostgresDetail(slots); got != "remaining connection slots are reserved, too_many_connections (SQLSTATE 53300)" {
		t.Errorf("slots = %q", got)
	}
	if got := cnpgPostgresDetail("ERROR: something odd (SQLSTATE 57P03)"); got != "cannot_connect_now (SQLSTATE 57P03)" {
		t.Errorf("code only = %q", got)
	}
	for _, unknown := range []string{"the instance manager crashed", "ERROR: relation \"secret_table\" does not exist (SQLSTATE 42P01)"} {
		if d := cnpgPostgresDetail(unknown); d != "" {
			t.Errorf("%q: unrecognized text must add nothing, got %q", unknown, d)
		}
	}
}

func TestCNPGPostgresDetailNeverEchoesTheError(t *testing.T) {
	leaks := []string{
		"pq: invalid connection string: postgresql://alice:secret@pg.private.example/db",
		"pq: out of shared memory while connecting with password='canary secret-tail' host=pg.private.example",
		"FATAL: password authentication failed for user \"alice\" (host = pg.private.example, password = secret tail)",
		"ERROR: could not connect to server pg.private.example at 10.1.2.3 as alice, secret tail (SQLSTATE 08001)",
		"FATAL: too many clients already from [2001:db8::1]:5432 user alice password secret-tail",
	}
	for _, raw := range leaks {
		err := apierrors.NewGenericServerResponse(500, "get", schema.GroupResource{Resource: "pods"}, "https:pg-1:8000", raw, 0, true)
		got, _ := cnpgRelayedPodSentence(err, cnpgStatusPort)
		for _, leak := range []string{"alice", "secret", "pg.private", "10.1.2.3", "2001:db8", "tail", "canary"} {
			if strings.Contains(got, leak) {
				t.Errorf("%q leaked %q: %q", raw, leak, got)
			}
		}
	}
}

func TestCNPGRelayedPodSentenceIgnoresAPIServerErrors(t *testing.T) {
	for _, err := range []error{
		apierrors.NewTimeoutError("request did not complete within requested timeout", 0),
		apierrors.NewServiceUnavailable("the server is currently unable to handle the request"),
		apierrors.NewInternalError(errors.New("etcd leader changed")),
		apierrors.NewGenericServerResponse(504, "get", schema.GroupResource{Resource: "pods"}, "https:pg-1:8000", "", 0, false),
	} {
		if got, ok := cnpgRelayedPodSentence(err, cnpgStatusPort); ok {
			t.Errorf("%v is the apiserver's own error, not the Pod's; got %q", err, got)
		}
	}
	timeout := apierrors.NewTimeoutError("request did not complete within requested timeout", 0)
	out := classifyCNPGProxyFailure(context.Background(), timeout, cnpgProxyOutcome{}, cnpgProxyTarget{namespace: "pg", pod: "pg-1", port: cnpgStatusPort, path: cnpgStatusPath})
	if strings.Contains(out.err, "instance manager") {
		t.Errorf("an apiserver timeout blamed the instance manager: %q", out.err)
	}
}

func TestCNPGNoPrometheusReasonIsOneSentence(t *testing.T) {
	for msg, want := range map[string]string{
		"": "Radar is not connected to Prometheus",
		"Radar found 2 services that may be Prometheus but may not port-forward to them (needs create pods/portforward)": "Radar found 2 services that may be Prometheus but may not port-forward to them (needs create pods/portforward)",
		"context deadline exceeded": "Radar is not connected to Prometheus: context deadline exceeded",
	} {
		if got := cnpgNoPrometheusReason(msg); got != want {
			t.Errorf("%q: %q, want %q", msg, got, want)
		}
	}
}
