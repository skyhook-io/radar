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
	exporter := apierrors.NewGenericServerResponse(503, "get", schema.GroupResource{Resource: "pods"}, "http:pg-1:9187", "collector failed", 0, true)
	cases := []struct {
		err  error
		port int
		want string
	}{
		{socketGone, cnpgStatusPort, "PostgreSQL is not running on this instance"},
		{other, cnpgStatusPort, "the instance manager could not read PostgreSQL's status: out of shared memory"},
		{exporter, cnpgMetricsPort, "the metrics endpoint on port 9187 answered with an error (HTTP 503)"},
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
	got := cnpgPostgresDetail(`an error on the server ("query failed for user=postgres database=app host=10.0.0.5:5432: FATAL: remaining connection slots are reserved (SQLSTATE 53300) via /controller/run/.s.PGSQL.5432") has prevented the request from succeeding`)
	if !strings.Contains(got, "remaining connection slots are reserved (SQLSTATE 53300)") {
		t.Errorf("detail = %q", got)
	}
	for _, leak := range []string{"user=", "database=", "10.0.0.5", ".s.PGSQL", "has prevented"} {
		if strings.Contains(got, leak) {
			t.Errorf("detail %q leaks %q", got, leak)
		}
	}
	if d := cnpgPostgresDetail("the instance manager crashed"); d != "" {
		t.Errorf("no PostgreSQL message, got %q", d)
	}
}
