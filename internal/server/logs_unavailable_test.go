package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// logAPIServer answers every request with the given status and body, the way
// the apiserver relays the kubelet's answer to a pod log request.
func logAPIServer(t *testing.T, status int, contentType, body string) *kubernetes.Clientset {
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
		name    string
		body    string
		want    []string
		wantErr error
	}{
		{"kubelet notice", "unable to retrieve container logs for containerd://6e2396b58f81ea5e", nil, k8score.ErrLogsUnavailable},
		{"last line without a newline", "2026-09-28T14:19:58.5Z database system is ready\n2026-09-28T14:19:59.5Z received fast shutdown request", []string{"database system is ready", "received fast shutdown request"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := logAPIServer(t, http.StatusOK, "", tc.body)
			logCh := make(chan workloadLogEntry, 10)
			err := followCNPGContainerLogs(context.Background(), client, "db", "pg-orders-2", corev1.PodLogOptions{Container: "postgres", Timestamps: true, Follow: true}, 1, logCh)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
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

func readSSEFor(t *testing.T, path string, window time.Duration) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), window)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, testServer.URL+path, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

func useLogsUnavailableServer(t *testing.T) {
	t.Helper()
	client := logAPIServer(t, http.StatusOK, "", "unable to retrieve container logs for containerd://6e2396b58f81ea5e")
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous) })
}

// The notice arrives together with EOF, so it has to be checked before the
// read error ends the stream.
func TestPodLogsStream_ReportsLogsUnavailable(t *testing.T) {
	useLogsUnavailableServer(t)
	stream := readSSEFor(t, "/api/pods/default/nginx-abc-xyz/logs/stream?container=nginx", 5*time.Second)
	if !strings.Contains(stream, "event: error") || !strings.Contains(stream, k8score.ErrLogsUnavailable.Error()) {
		t.Fatalf("expected an error event naming the reason:\n%s", stream)
	}
	if strings.Contains(stream, "event: log") || strings.Contains(stream, "event: end") {
		t.Fatalf("the notice must not arrive as a log line or a clean end:\n%s", stream)
	}
}

// The workload live view keeps running and reopens each source every discovery
// pass. A source the node cannot read must be named once in the notice bar, not
// left as a silently empty pane.
func TestWorkloadLogsStream_ReportsLogsUnavailableOnce(t *testing.T) {
	useLogsUnavailableServer(t)
	stream := readSSEFor(t, "/api/workloads/deployments/default/nginx/logs/stream", 7*time.Second)
	want := "1 source could not be read: nginx-abc-xyz/nginx: " + k8score.ErrLogsUnavailable.Error()
	if !strings.Contains(stream, "event: notice") || !strings.Contains(stream, want) {
		t.Fatalf("expected a notice naming the source:\n%s", stream)
	}
	if n := strings.Count(stream, "event: notice"); n != 1 {
		t.Fatalf("notice sent %d times across a discovery pass, want once:\n%s", n, stream)
	}
	if strings.Contains(stream, "event: log") {
		t.Fatalf("the notice must not arrive as a log line:\n%s", stream)
	}
}

// The CNPG live view reopens each instance's stream every discovery pass. An
// instance the node cannot read must be named once, not shown as a log line or
// left silently empty.
func TestCNPGClusterLogsStream_ReportsLogsUnavailableOnce(t *testing.T) {
	seedCNPGLogCluster(t, "pgunavailable")
	useLogsUnavailableServer(t)
	stream := readSSEFor(t, "/api/cnpg/clusters/pgunavailable/pg-orders/logs/stream?pod=pg-orders-2", 7*time.Second)
	want := "1 source could not be read: pg-orders-2/postgres: " + k8score.ErrLogsUnavailable.Error()
	if !strings.Contains(stream, "event: notice") || !strings.Contains(stream, want) {
		t.Fatalf("expected a notice naming the instance:\n%s", stream)
	}
	if n := strings.Count(stream, "event: notice"); n != 1 {
		t.Fatalf("notice sent %d times across a discovery pass, want once:\n%s", n, stream)
	}
	if strings.Contains(stream, "event: log") {
		t.Fatalf("the notice must not arrive as a log line:\n%s", stream)
	}
}
