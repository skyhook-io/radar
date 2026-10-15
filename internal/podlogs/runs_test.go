package podlogs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/pkg/k8score"
)

// readAnswer returns the error a log read gets when the apiserver answers with
// the given 400 message, the way it relays the kubelet's refusal.
func readAnswer(t *testing.T, message string) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"kind": "Status", "apiVersion": "v1", "status": "Failure", "message": message, "reason": "BadRequest", "code": 400})
	}))
	t.Cleanup(srv.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = k8score.GetContainerLogs(context.Background(), client, "default", "api-0", "app", k8score.LogOptions{})
	return err
}

func TestIsNoPreviousRun(t *testing.T) {
	if !IsNoPreviousRun(readAnswer(t, `previous terminated container "app" in pod "api-0" not found`)) {
		t.Error("the apiserver's no-earlier-run answer should be recognised")
	}
	if IsNoPreviousRun(errors.New(`previous terminated container "app" in pod "api-0" not found`)) {
		t.Error("only the apiserver's 400 answer should be recognised, not any error with the same words")
	}
	if IsNoPreviousRun(errors.New("connection refused")) || IsNoPreviousRun(nil) {
		t.Error("an unrelated failure or no error is not a missing earlier run")
	}
}

// The messages are the kubelet's, captured from a kind cluster.
func TestIsContainerNotStarted(t *testing.T) {
	for _, message := range []string{
		`container "app" in pod "api-0" is waiting to start: ContainerCreating`,
		`container "app" in pod "api-0" is waiting to start: image can't be pulled`,
		`container "app" in pod "api-0" is waiting to start: trying and failing to pull image`,
		`container "app" in pod "api-0" is waiting to start: CreateContainerConfigError`,
		`container "app" in pod "api-0" is waiting to start - no logs yet`,
	} {
		if !IsContainerNotStarted(readAnswer(t, message)) {
			t.Errorf("not recognised: %s", message)
		}
	}
	if IsContainerNotStarted(readAnswer(t, `previous terminated container "app" in pod "api-0" not found`)) {
		t.Error("a missing earlier run is a different answer")
	}
	if IsContainerNotStarted(errors.New(`container "app" in pod "api-0" is waiting to start: ContainerCreating`)) {
		t.Error("only the apiserver's 400 answer should be recognised")
	}
	if IsContainerNotStarted(nil) {
		t.Error("no error is not a container that has not started")
	}
}

func TestEarlierRun(t *testing.T) {
	terminated := corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}}
	for _, tc := range []struct {
		name       string
		pod        *corev1.Pod
		had, known bool
	}{
		{"never restarted", &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "app"}}}}, false, true},
		{"current run ended is not an earlier run", &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "app", State: terminated}}}}, false, true},
		{"restart count", &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "app", RestartCount: 1}}}}, true, true},
		{"last termination without a restart count", &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "app", LastTerminationState: terminated}}}}, true, true},
		{"init container", &corev1.Pod{Status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{{Name: "app", RestartCount: 2}}}}, true, true},
		{"no status yet", &corev1.Pod{}, false, false},
		{"no pod", nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			had, known := EarlierRun(tc.pod, "app")
			if had != tc.had || known != tc.known {
				t.Fatalf("EarlierRun = (%t, %t), want (%t, %t)", had, known, tc.had, tc.known)
			}
		})
	}
}

func TestNotStartedMessage(t *testing.T) {
	waiting := func(reason, message string) *corev1.Pod {
		return &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: message}},
		}}}}
	}
	answer := errors.New(`failed to stream logs for default/api-0/app: container "app" in pod "api-0" is waiting to start: trying and failing to pull image`)
	for _, tc := range []struct {
		name string
		pod  *corev1.Pod
		err  error
		want string
	}{
		{"status reason and message", waiting("CreateContainerConfigError", `configmap "missing-config" not found`), answer,
			`This container has not started yet, so it has no logs. Kubernetes reports CreateContainerConfigError: configmap "missing-config" not found.`},
		{"status reason only", waiting("ContainerCreating", ""), answer,
			"This container has not started yet, so it has no logs. Kubernetes reports ContainerCreating."},
		{"no status falls back to the apiserver's answer", nil, answer,
			"This container has not started yet, so it has no logs. Kubernetes reports trying and failing to pull image."},
		{"nothing to add", nil, errors.New(`container "app" in pod "api-0" is waiting to start - no logs yet`),
			"This container has not started yet, so it has no logs."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := NotStartedMessage(tc.pod, "app", tc.err); got != tc.want {
				t.Fatalf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestContainerNotStartedYet(t *testing.T) {
	waiting := corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}
	pod := &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
		{Name: "pulling", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}},
		{Name: "running", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
		{Name: "restarted", State: waiting, RestartCount: 1},
		{Name: "crashed-once", State: waiting, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}}},
	}}}
	if !ContainerNotStartedYet(pod, "pulling") {
		t.Error("pulling: want not started")
	}
	for _, name := range []string{"running", "restarted", "crashed-once"} {
		if ContainerNotStartedYet(pod, name) {
			t.Errorf("%s: has run, so it has logs", name)
		}
	}
	// No status means Radar can't tell, so the caller asks the apiserver.
	if ContainerNotStartedYet(pod, "unknown") || ContainerNotStartedYet(nil, "pulling") {
		t.Error("no status: want false")
	}
}
