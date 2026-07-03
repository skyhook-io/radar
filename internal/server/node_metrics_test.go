package server

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
)

func TestListPodsScoped_SentinelContract(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns1"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "ns2"}},
	)
	f := informers.NewSharedInformerFactory(client, 0)
	informer := f.Core().V1().Pods()
	_ = informer.Informer()
	stop := make(chan struct{})
	defer close(stop)
	f.Start(stop)
	f.WaitForCacheSync(stop)
	lister := informer.Lister()

	if got := listPodsScoped(lister, nil); len(got) != 2 {
		t.Errorf("nil scope = %d pods, want 2 (cluster-wide)", len(got))
	}
	if got := listPodsScoped(lister, []string{}); len(got) != 0 {
		t.Errorf("empty scope = %d pods, want 0 (no namespace access)", len(got))
	}
	if got := listPodsScoped(lister, []string{"ns1"}); len(got) != 1 {
		t.Errorf("ns1 scope = %d pods, want 1", len(got))
	}
	if got := listPodsScoped(nil, nil); got != nil {
		t.Errorf("nil lister must yield nil")
	}
}

func TestComputeCapacityRequests_ExcludesCompletedPods(t *testing.T) {
	nodes := []*corev1.Node{{Status: corev1.NodeStatus{Capacity: corev1.ResourceList{
		corev1.ResourceCPU: mustQty(t, "4"), corev1.ResourceMemory: mustQty(t, "8Gi"),
	}}}}
	req := corev1.ResourceList{corev1.ResourceCPU: mustQty(t, "500m"), corev1.ResourceMemory: mustQty(t, "1Gi")}
	running := &corev1.Pod{
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{Requests: req}}}},
	}
	done := running.DeepCopy()
	done.Status.Phase = corev1.PodSucceeded

	cr := computeCapacityRequests(nodes, []*corev1.Pod{running, done})
	if cr.cpuCapMillis != 4000 {
		t.Errorf("cpuCap = %d, want 4000", cr.cpuCapMillis)
	}
	if cr.cpuReqMillis != 500 {
		t.Errorf("cpuReq = %d, want 500 (completed pod excluded)", cr.cpuReqMillis)
	}
}

func mustQty(t *testing.T, s string) resource.Quantity {
	t.Helper()
	q, err := resource.ParseQuantity(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return q
}
