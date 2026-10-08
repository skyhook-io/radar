package server

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/traffic"
)

func TestResolveFlowWorkloads(t *testing.T) {
	yes := true
	client := fake.NewSimpleClientset(
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web-7d9f", OwnerReferences: []metav1.OwnerReference{
			{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", Controller: &yes},
		}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web-7d9f-x2k4q", OwnerReferences: []metav1.OwnerReference{
			{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-7d9f", Controller: &yes},
		}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "debug"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "etcd-node-1", OwnerReferences: []metav1.OwnerReference{
			{APIVersion: "v1", Kind: "Node", Name: "node-1", Controller: &yes},
		}}},
	)
	useTestResourceCache(t, client)

	flows := []traffic.Flow{{
		Source:      traffic.Endpoint{Namespace: "shop", Name: "web-7d9f-x2k4q", Kind: traffic.EndpointKindPod, Workload: "web-7d9f", WorkloadKind: "ReplicaSet"},
		Destination: traffic.Endpoint{Namespace: "shop", Name: "debug", Kind: traffic.EndpointKindPod, Workload: "stale-guess", WorkloadKind: "Deployment"},
	}, {
		Source:      traffic.Endpoint{Namespace: "shop", Name: "gone-abc", Kind: traffic.EndpointKindPod, Workload: "gone", WorkloadKind: "Deployment"},
		Destination: traffic.Endpoint{Namespace: "kube-system", Name: "etcd-node-1", Kind: traffic.EndpointKindPod},
	}}
	got := resolveFlowWorkloads(k8s.GetResourceCache(), flows)

	if s := got[0].Source; s.Workload != "web" || s.WorkloadKind != "Deployment" {
		t.Errorf("source workload = %s/%s, want the Deployment through its ReplicaSet", s.WorkloadKind, s.Workload)
	}
	if d := got[0].Destination; d.Workload != "" {
		t.Errorf("unowned pod workload = %q, want none: the cache says nothing owns it", d.Workload)
	}
	if s := got[1].Source; s.Workload != "gone" {
		t.Errorf("uncached pod workload = %q, want the source's answer kept", s.Workload)
	}
	if d := got[1].Destination; d.Workload != "" {
		t.Errorf("static pod workload = %s/%s, want none: its Node is not a workload", d.WorkloadKind, d.Workload)
	}
	if got[0].Source.Name != "web-7d9f-x2k4q" {
		t.Error("records keep their pod names")
	}
}
