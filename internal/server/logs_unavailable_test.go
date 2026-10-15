package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/podlogs"
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
			logCh := make(chan podlogs.Entry, 10)
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

const waitingAnswer = `{"kind":"Status","apiVersion":"v1","status":"Failure","message":"container \"postgres\" in pod \"pulling\" is waiting to start: trying and failing to pull image","reason":"BadRequest","code":400}`

func waitingPod(name string) *corev1.Pod {
	pod := cnpgPod("notstarted", name, "x")
	pod.Status.Phase = corev1.PodPending
	pod.Status.ContainerStatuses[0] = corev1.ContainerStatus{
		Name:  "postgres",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: `Back-off pulling image "pg:nope"`}},
	}
	return pod
}

// A container that has not started (an image that cannot be pulled, a missing
// ConfigMap, ContainerCreating) is a fact about the pod. It reached the viewer
// as a 500, which told the reader Radar had broken.
func TestPodLogs_ContainerNotStartedIsAnAnswer(t *testing.T) {
	seedCNPGPods(t, waitingPod("pulling"))
	client := logAPIServer(t, http.StatusBadRequest, "application/json", waitingAnswer)
	prev := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(prev) })

	status, body := getPodLogsError(t, "/api/pods/notstarted/pulling/logs?container=postgres")
	want := `This container has not started yet, so it has no logs. Kubernetes reports ImagePullBackOff: Back-off pulling image "pg:nope".`
	if status != http.StatusNotFound || body["error_code"] != "container_not_started" || body["error"] != want {
		t.Fatalf("got %d %v, want 404 container_not_started %q", status, body, want)
	}
}

// The live view of a pending pod used to end on a red error. It now waits for
// the container, says why once, clears that when the container starts, and
// then streams its lines.
func TestPodLogsStream_WaitsForContainerToStart(t *testing.T) {
	pod := waitingPod("pulling")
	seedCNPGPods(t, pod)
	prevInterval := containerStartPollInterval
	containerStartPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { containerStartPollInterval = prevInterval })

	var started atomic.Bool
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if !started.Load() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(waitingAnswer))
			return
		}
		_, _ = w.Write([]byte("2026-10-14T10:00:00Z database system is ready\n"))
	}))
	t.Cleanup(srv.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	prev := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(prev) })

	// The container starts after a while: the pod cache sees it running.
	go func() {
		time.Sleep(400 * time.Millisecond)
		if n := requests.Load(); n != 1 {
			t.Errorf("apiserver asked %d times while the cache showed the container waiting, want once", n)
		}
		started.Store(true)
		running := pod.DeepCopy()
		running.Status.Phase = corev1.PodRunning
		running.Status.ContainerStatuses[0].State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
		if _, err := testFakeClient.CoreV1().Pods(running.Namespace).UpdateStatus(context.Background(), running, metav1.UpdateOptions{}); err != nil {
			t.Errorf("update pod status: %v", err)
		}
	}()

	stream := readSSEFor(t, "/api/pods/notstarted/pulling/logs/stream?container=postgres", 3*time.Second)
	if strings.Contains(stream, "event: error") {
		t.Fatalf("a container that has not started must not end the stream with an error:\n%s", stream)
	}
	if n := strings.Count(stream, `"notice_code":"container_not_started"`); n != 1 {
		t.Fatalf("waiting notice sent %d times, want once:\n%s", n, stream)
	}
	if !strings.Contains(stream, "ImagePullBackOff") || !strings.Contains(stream, "Logs will show here once it starts.") {
		t.Fatalf("the notice must say why and what happens next:\n%s", stream)
	}
	cleared := strings.Index(stream, `{"notice":""}`)
	line := strings.Index(stream, "database system is ready")
	if cleared < 0 || line < 0 || cleared > line {
		t.Fatalf("the notice must be cleared before the first line:\n%s", stream)
	}
}

// A container can wait minutes for an image. The notice is sent once, so the
// stream must still write something periodically or an idle-timeout proxy
// closes it before the first line arrives.
func TestPodLogsStream_WaitingStreamSendsKeepalives(t *testing.T) {
	seedCNPGPods(t, waitingPod("pulling"))
	prevInterval, prevKeepalive := containerStartPollInterval, waitingKeepaliveInterval
	containerStartPollInterval, waitingKeepaliveInterval = 20*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { containerStartPollInterval, waitingKeepaliveInterval = prevInterval, prevKeepalive })

	client := logAPIServer(t, http.StatusBadRequest, "application/json", waitingAnswer)
	prev := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(prev) })

	stream := readSSEFor(t, "/api/pods/notstarted/pulling/logs/stream?container=postgres", 400*time.Millisecond)
	if n := strings.Count(stream, ": waiting\n"); n < 2 {
		t.Fatalf("got %d keepalive comments in 400ms, want at least 2:\n%s", n, stream)
	}
	if n := strings.Count(stream, `"notice_code":"container_not_started"`); n != 1 {
		t.Fatalf("waiting notice sent %d times, want once:\n%s", n, stream)
	}
}
