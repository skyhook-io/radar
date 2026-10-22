package server

import (
	"bufio"
	"context"
	"fmt"
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

func TestWorkloadLogsStreamReplacesActivePodUID(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "replace-0", Namespace: "replace", UID: "old", Labels: map[string]string{"app": "replace"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	cluster := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "replace"}}, &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "replace", Namespace: "replace"}, Spec: appsv1.StatefulSetSpec{Selector: &metav1.LabelSelector{MatchLabels: pod.Labels}}}, pod)
	useTestResourceCache(t, cluster)
	var opens atomic.Int32
	oldCanceled := make(chan struct{})
	secondOptions := make(chan string, 1)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ordinal := opens.Add(1)
		if ordinal == 1 {
			fmt.Fprintln(w, "2026-10-13T10:00:05Z old")
		} else {
			secondOptions <- r.URL.RawQuery
			fmt.Fprintln(w, "2026-10-13T10:00:00Z new")
		}
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		if ordinal == 1 {
			close(oldCanceled)
		}
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
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, testServer.URL+"/api/workloads/statefulsets/replace/replace/logs/stream", nil)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })
	lines := make(chan string, 64)
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
	waitFor := func(content string) {
		t.Helper()
		timer := time.NewTimer(9 * time.Second)
		defer timer.Stop()
		for {
			select {
			case line, ok := <-lines:
				if !ok {
					t.Fatalf("stream ended before %s", content)
				}
				if strings.Contains(line, content) {
					return
				}
			case <-timer.C:
				t.Fatalf("stream never produced %s", content)
			}
		}
	}
	waitFor(`"content":"old"`)
	replacement := pod.DeepCopy()
	replacement.UID = "new"
	if _, err := cluster.CoreV1().Pods("replace").Update(ctx, replacement, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(`"content":"new"`)
	select {
	case <-oldCanceled:
	case <-time.After(time.Second):
		t.Fatal("replacement did not cancel the old active follow")
	}
	select {
	case options := <-secondOptions:
		if strings.Contains(options, "sinceTime") || !strings.Contains(options, "tailLines=50") {
			t.Fatalf("replacement reused old cursor: %s", options)
		}
	case <-time.After(time.Second):
		t.Fatal("no replacement follow request")
	}
	// An old worker finishing must not remove the replacement's active handle.
	time.Sleep(5500 * time.Millisecond)
	if count := opens.Load(); count != 2 {
		t.Fatalf("replacement follow was reopened while active: %d requests", count)
	}
	cancel()
}
