package server

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func TestWorkloadLogsStream_RepeatedWatcherFailureKeepsNotice(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		t.Run(fmt.Sprintf("fresh=%v", fresh), func(t *testing.T) {
			var requests atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ordinal := requests.Add(1)
				if !fresh {
					ordinal = 1
				}
				_, _ = fmt.Fprintf(w, "2026-10-20T10:00:%02dZ application record %d\n", ordinal, ordinal)
				w.(http.Flusher).Flush()
				// Allow Radar to process the replay before the node fails its follow.
				select {
				case <-time.After(150 * time.Millisecond):
				case <-r.Context().Done():
					return
				}
				_, _ = io.WriteString(w, "failed to create fsnotify watcher: too many open files\n")
			}))
			t.Cleanup(api.Close)
			client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL})
			if err != nil {
				t.Fatal(err)
			}
			previous := k8s.SetTestClient(client)
			t.Cleanup(func() { k8s.SetTestClient(previous) })
			stream := readSSEFor(t, "/api/workloads/deployments/default/nginx/logs/stream", 7*time.Second)
			if requests.Load() < 2 || strings.Contains(stream, `"notice":""`) || strings.Count(stream, "event: notice") != 1 {
				t.Fatalf("expected one persistent warning across failed follows:\n%s", stream)
			}
		})
	}
}

func TestWorkloadLogsStream_RecoveryRequiresOpenedFollowOrFiniteRead(t *testing.T) {
	for _, mode := range []string{"idle", "pending", "finished"} {
		t.Run(mode, func(t *testing.T) {
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "watcher-0", Namespace: "watcher", UID: "one", Labels: map[string]string{"app": "watcher"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
			if mode == "finished" {
				pod.Status.Phase = corev1.PodSucceeded
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}}}
			}
			inventory := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "watcher"}}, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "watcher", Namespace: "watcher"}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: pod.Labels}}}, pod)
			useTestResourceCache(t, inventory)
			var requests atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == 1 {
					_, _ = io.WriteString(w, "failed to create fsnotify watcher: too many open files\n")
					return
				}
				if mode != "pending" {
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
				}
				if mode != "finished" {
					<-r.Context().Done()
				}
			}))
			t.Cleanup(api.Close)
			client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL})
			if err != nil {
				t.Fatal(err)
			}
			previous := k8s.SetTestClient(client)
			t.Cleanup(func() { k8s.SetTestClient(previous) })
			stream := readSSEFor(t, "/api/workloads/deployments/watcher/watcher/logs/stream", 12*time.Second)
			cleared := strings.Contains(stream, `"notice":""`)
			if requests.Load() < 2 || cleared != (mode != "pending") {
				t.Fatalf("mode=%s, requests=%d, cleared=%v:\n%s", mode, requests.Load(), cleared, stream)
			}
		})
	}
}
