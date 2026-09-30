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
