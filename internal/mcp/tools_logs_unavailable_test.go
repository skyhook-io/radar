package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// useLogsUnavailableServer answers every log read with the kubelet's notice,
// which the apiserver relays with a 200 as the whole body.
func useLogsUnavailableServer(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("unable to retrieve container logs for containerd://6e2396b58f81ea5e"))
	}))
	t.Cleanup(server.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous) })
}

func TestGetPodLogsReportsLogsUnavailable(t *testing.T) {
	useLogsUnavailableServer(t)
	_, _, err := handleGetPodLogs(context.Background(), nil, podLogsInput{Namespace: "shop", Name: "api", Container: "app"})
	if !errors.Is(err, k8score.ErrLogsUnavailable) {
		t.Fatalf("expected the logs-unavailable error, got %v", err)
	}
}

func TestFetchPodLogsReportsLogsUnavailable(t *testing.T) {
	useLogsUnavailableServer(t)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "api-0", Namespace: "shop"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "api"}}},
	}
	entries := fetchPodLogs(context.Background(), []*corev1.Pod{pod}, "shop", "api", "", 100, nil, false)
	if len(entries) != 1 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].Error != k8score.ErrLogsUnavailable.Error() || len(entries[0].Logs.Lines) != 0 {
		t.Fatalf("the notice must be reported as the read's error, not a log line: %+v", entries[0])
	}
}
