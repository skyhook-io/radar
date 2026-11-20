package server

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestWorkloadLogsStreamSendsPodStatusChanges(t *testing.T) {
	setWorkloadLogDiscoveryInterval(t, 100*time.Millisecond)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "predictor-0", Namespace: "serve", UID: "uid-1", Labels: map[string]string{"app": "predictor"}},
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{Name: "storage-initializer"}},
			Containers:     []corev1.Container{{Name: "kserve-container"}},
		},
		Status: corev1.PodStatus{
			Phase:                 corev1.PodPending,
			InitContainerStatuses: []corev1.ContainerStatus{{Name: "storage-initializer", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}},
			ContainerStatuses:     []corev1.ContainerStatus{{Name: "kserve-container", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}}}},
		},
	}
	cluster := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "serve"}}, &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "predictor", Namespace: "serve"}, Spec: appsv1.StatefulSetSpec{Selector: &metav1.LabelSelector{MatchLabels: pod.Labels}}}, pod)
	useTestResourceCache(t, cluster)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "2026-11-08T00:00:00Z downloading")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(api.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous) })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, testServer.URL+"/api/workloads/statefulsets/serve/predictor/logs/stream", nil)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })
	lines := make(chan string, 256)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	// waitFor returns the data line that follows the named event.
	waitFor := func(event, content string) {
		t.Helper()
		timer := time.NewTimer(9 * time.Second)
		defer timer.Stop()
		inEvent := false
		for {
			select {
			case line, ok := <-lines:
				if !ok {
					t.Fatalf("stream ended before %s %s", event, content)
				}
				if strings.HasPrefix(line, "event: ") {
					inEvent = line == "event: "+event
					continue
				}
				if inEvent && strings.Contains(line, content) {
					return
				}
			case <-timer.C:
				t.Fatalf("stream never sent %s with %s", event, content)
			}
		}
	}
	waitFor("connected", `"reason":"PodInitializing"`)

	// A few discovery passes with no change send nothing.
	time.Sleep(3 * workloadLogDiscoveryInterval)
	for drained := false; !drained; {
		select {
		case line := <-lines:
			if line == "event: pod_updated" {
				t.Fatal("pod_updated sent without a status change")
			}
		default:
			drained = true
		}
	}

	started := true
	initialized := pod.DeepCopy()
	initialized.Status.Phase = corev1.PodRunning
	initialized.Status.InitContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed"}}
	initialized.Status.ContainerStatuses[0] = corev1.ContainerStatus{Name: "kserve-container", Ready: true, Started: &started, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
	if _, err := cluster.CoreV1().Pods("serve").UpdateStatus(ctx, initialized, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor("pod_updated", `"name":"kserve-container","ready":true,"restartCount":0,"state":"running","started":true`)
	cancel()
}
