package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/pkg/k8score"
)

// logAPIServer answers every request with the given status and body, the way
// the apiserver relays the kubelet's answer to a pod log request.
func logAPIServer(t *testing.T, status int, contentType, body string) kubernetes.Interface {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// The kubelet answers with a 200 whose entire body is its own apology when it
// no longer holds a container's log file. Without recognising it, that sentence
// reaches the reader as a line their workload appears to have printed.
func TestIsLogsUnavailableNotice(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"kubelet apology", "unable to retrieve container logs for containerd://6e2396b58f81ea5e", true},
		{"with surrounding whitespace", "  unable to retrieve container logs for docker://abc\n", true},
		{"a real line that happens to mention retrieving", "2026-09-18T05:00:00Z unable to retrieve config from vault", false},
		{"ordinary output", "2026-09-18T05:00:00Z checkout serving request 1", false},
		{"empty", "", false},
	} {
		if got := isLogsUnavailableNotice(tc.body); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Asking for an earlier run of a container that never restarted is an ordinary
// fact about the pod. It arrives as a 400 from the apiserver, and surfacing it
// as a 500 told the reader Radar had broken.
func TestIsNoPreviousContainer(t *testing.T) {
	client := logAPIServer(t, http.StatusBadRequest, "application/json",
		`{"kind":"Status","apiVersion":"v1","status":"Failure","message":"previous terminated container \"app\" in pod \"api-0\" not found","reason":"BadRequest","code":400}`)
	_, err := k8score.GetContainerLogs(context.Background(), client, "default", "api-0", "app", k8score.LogOptions{Previous: true})
	if !isNoPreviousContainer(err) {
		t.Errorf("the apiserver's no-earlier-run answer should be recognised, got %v", err)
	}
	if isNoPreviousContainer(errors.New(`previous terminated container "app" in pod "api-0" not found`)) {
		t.Error("only the apiserver's 400 answer should be recognised, not any error with the same words")
	}
	if isNoPreviousContainer(errors.New("connection refused")) {
		t.Error("an unrelated failure must not be reported as a missing earlier run")
	}
	if isNoPreviousContainer(nil) {
		t.Error("no error is not a missing earlier run")
	}
}

// The CNPG live view emits a stream's last line even when it arrives with the
// end of the stream, so the kubelet's notice used to show up as a Postgres line,
// again every time the view reopened the stream.
func TestFollowCNPGContainerLogs_DropsLogsUnavailableNotice(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want []string
	}{
		{"kubelet notice", "unable to retrieve container logs for containerd://6e2396b58f81ea5e", nil},
		{"last line without a newline", "2026-09-28T14:19:58.5Z database system is ready\n2026-09-28T14:19:59.5Z received fast shutdown request", []string{"database system is ready", "received fast shutdown request"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := logAPIServer(t, http.StatusOK, "", tc.body)
			logCh := make(chan workloadLogEntry, 10)
			followCNPGContainerLogs(context.Background(), client, "db", "pg-orders-2", corev1.PodLogOptions{Container: "postgres", Timestamps: true, Follow: true}, logCh)
			close(logCh)
			var got []string
			for entry := range logCh {
				got = append(got, entry.Content)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %q, want %q", got, tc.want)
				}
			}
		})
	}
}
