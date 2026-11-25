package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/podlogs"
)

func TestReplicaSetLogsStayWithTheSelectedRevision(t *testing.T) {
	const ns = "shop"
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-v1", Namespace: ns, UID: "v1"},
		Spec:       appsv1.ReplicaSetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "checkout"}}},
	}
	sibling := rs.DeepCopy()
	sibling.Name, sibling.UID = "checkout-v2", "v2"
	pod := func(name string, owner *appsv1.ReplicaSet) *corev1.Pod {
		p := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID(name), Labels: rs.Spec.Selector.MatchLabels},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		}
		if owner != nil {
			p.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(owner, appsv1.SchemeGroupVersion.WithKind("ReplicaSet"))}
		}
		return p
	}
	recreated := rs.DeepCopy()
	recreated.UID = "new-incarnation"
	inactive := rs.DeepCopy()
	inactive.Name, inactive.UID = "checkout-old", "old"
	zero := int32(0)
	inactive.Spec.Replicas = &zero
	objects := []runtime.Object{rs, sibling, inactive, pod("ours", rs), pod("sibling", sibling), pod("bare", nil), pod("other-incarnation", recreated)}
	cluster := fake.NewClientset(append(testNamespaces(ns), objects...)...)
	useTestResourceCache(t, cluster)
	client := logAPIServer(t, http.StatusOK, "text/plain", "2026-10-20T10:00:00Z hello\n")
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous) })
	const path = "/api/workloads/replicasets/shop/checkout-v1/logs"
	resp, err := http.Get(testServer.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var snapshot struct{ Logs []podlogs.Entry }
	if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil || len(snapshot.Logs) != 1 || snapshot.Logs[0].Pod != "ours" {
		t.Fatalf("snapshot = %+v (status %d, error %v)", snapshot, resp.StatusCode, err)
	}

	empty := readSSEFor(t, "/api/workloads/ReplicaSet/shop/checkout-old/logs/stream", 3*time.Second)
	if !strings.Contains(empty, `"emptyMessage":"This ReplicaSet currently controls no Pods."`) || strings.Contains(empty, "event: log\n") {
		t.Fatalf("inactive ReplicaSet stream: %s", empty)
	}

	setWorkloadLogDiscoveryInterval(t, 50*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, testServer.URL+path+"/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	deleted := false
	added := false
	scanner := bufio.NewScanner(stream.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, `"sibling"`) || strings.Contains(line, `"bare"`) || strings.Contains(line, `"other-incarnation"`) {
			t.Fatalf("observed another controller's pod: %s", line)
		}
		if !deleted && strings.Contains(line, `"pod":"ours"`) {
			deleted = true
			if err := cluster.CoreV1().Pods(ns).Delete(ctx, "ours", metav1.DeleteOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		if deleted && !added && strings.Contains(line, `"reason":"terminated"`) {
			added = true
			if _, err := cluster.CoreV1().Pods(ns).Create(ctx, pod("replacement", rs), metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		if strings.Contains(line, `"pod":"replacement"`) {
			return
		}
	}
	t.Fatalf("stream ended before the replacement's logs: %v", scanner.Err())
}
