package topology

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPodRelationshipsNameTheDeploymentAboveTheirReplicaSet(t *testing.T) {
	ctrl := true
	owner := func(apiVersion, kind, name string) []metav1.OwnerReference {
		return []metav1.OwnerReference{{APIVersion: apiVersion, Kind: kind, Name: name, Controller: &ctrl}}
	}
	provider := &mockProvider{
		deployments: []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "team"}}},
		replicaSets: []*appsv1.ReplicaSet{{ObjectMeta: metav1.ObjectMeta{Name: "web-abc", Namespace: "team", OwnerReferences: owner("apps/v1", "Deployment", "web")}}},
		pods:        []*corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "web-abc-1", Namespace: "team", OwnerReferences: owner("apps/v1", "ReplicaSet", "web-abc")}}},
	}
	opts := DefaultBuildOptions()
	opts.IncludeReplicaSets = true
	topo, err := NewBuilder(provider).Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	rel := GetRelationshipsWithObject("Pod", "team", "web-abc-1", nil, topo, nil, nil, IndexByResource(topo))
	if rel == nil || rel.Deployment == nil || rel.Deployment.Name != "web" {
		t.Fatalf("Pod relationships = %+v, want Deployment web above its ReplicaSet", rel)
	}
	dep := GetRelationshipsWithObject("Deployment", "team", "web", nil, topo, nil, nil, IndexByResource(topo))
	if dep == nil || len(dep.Pods) != 1 || dep.Pods[0].Name != "web-abc-1" {
		t.Errorf("Deployment pods = %+v", dep)
	}
}
