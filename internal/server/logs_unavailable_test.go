package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	if !strings.Contains(stream, `"error_code":"logs_unavailable"`) {
		t.Fatalf("the error event must carry the code the viewer shows it by:\n%s", stream)
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

func getPodLogsError(t *testing.T, path string) (int, map[string]string) {
	t.Helper()
	resp, err := http.Get(testServer.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return resp.StatusCode, body
}

// The apiserver answers "previous terminated container not found" both for a
// container that never restarted and for one whose earlier run the node no
// longer has. Telling a reader whose pod shows restarts that it never
// restarted is false, so the restart count picks the answer.
func TestPodLogs_PreviousRunAnswerFollowsRestartCount(t *testing.T) {
	never := cnpgPod("prevlogs", "never-restarted", "x")
	never.Status.ContainerStatuses[0].RestartCount = 0
	restarted := cnpgPod("prevlogs", "restarted", "x")
	restarted.Status.ContainerStatuses[0].RestartCount = 1
	seedCNPGPods(t, never, restarted)
	client := logAPIServer(t, http.StatusBadRequest, "application/json",
		`{"kind":"Status","apiVersion":"v1","status":"Failure","message":"previous terminated container \"postgres\" in pod \"p\" not found","reason":"BadRequest","code":400}`)
	prev := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(prev) })

	for _, tc := range []struct {
		pod, code, message string
	}{
		{"never-restarted", "no_previous_run", "This container has not restarted, so there is no earlier run to show."},
		{"restarted", "logs_unavailable", "Kubernetes did not return the logs of this container's earlier run."},
	} {
		t.Run(tc.pod, func(t *testing.T) {
			status, body := getPodLogsError(t, "/api/pods/prevlogs/"+tc.pod+"/logs?container=postgres&previous=true")
			if status != http.StatusNotFound || body["error_code"] != tc.code || body["error"] != tc.message {
				t.Fatalf("got %d %v, want 404 %s %q", status, body, tc.code, tc.message)
			}
		})
	}
}

// The node's "could not read logs" answer is about the cluster, not a Radar
// failure, so it carries a code the viewer can show it by.
func TestPodLogs_LogsUnavailableCarriesCode(t *testing.T) {
	useLogsUnavailableServer(t)
	status, body := getPodLogsError(t, "/api/pods/default/nginx-abc-xyz/logs?container=nginx")
	if status != http.StatusNotFound || body["error_code"] != "logs_unavailable" || body["error"] != k8score.ErrLogsUnavailable.Error() {
		t.Fatalf("got %d %v", status, body)
	}
}
