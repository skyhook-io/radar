package topology

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

func TestRejectedFluxHintPreservesLowerPriorityEvidence(t *testing.T) {
	graph := &Topology{Nodes: []Node{
		{ID: "configmap/app/config", Kind: KindConfigMap, Name: "config", Data: map[string]any{"namespace": "app"}},
		{ID: "helmrelease/gitops/stale", Kind: KindHelmRelease, Name: "stale", gitopsManagementAuthoritative: true, Data: map[string]any{"namespace": "gitops", "apiVersion": fluxHelmGroup + "/v2"}},
		{ID: "kustomization/gitops/actual", Kind: KindKustomization, Name: "actual", gitopsManagementAuthoritative: true, managedResourceKeys: map[string]bool{resourceid.ResourceKey("", "ConfigMap", "app", "config"): true}, Data: map[string]any{"namespace": "gitops", "apiVersion": fluxKustomizeGroup + "/v1"}},
	}}
	for _, tc := range []struct {
		name, kind, group, manager string
		labels, annotations        map[string]string
	}{
		{"Kustomization", "Kustomization", fluxKustomizeGroup, "actual", map[string]string{fluxKustomizeNameLabel: "actual", fluxKustomizeNSLabel: "gitops"}, nil},
		{"Argo", "Application", argoApplicationGroup, "new-app", nil, map[string]string{argoTrackingIDAnnotation: "gitops_new-app:/ConfigMap:app/config"}},
		{"native Helm", "HelmRelease", "", "native", nil, map[string]string{helmReleaseNameAnno: "native", helmReleaseNSAnno: "app"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "config", Namespace: "app", Labels: map[string]string{fluxHelmNameLabel: "stale", fluxHelmNSLabel: "gitops"}, Annotations: tc.annotations}}
			for key, value := range tc.labels {
				obj.Labels[key] = value
			}
			for _, indexed := range []bool{false, true} {
				var idx *RelationshipsIndex
				if indexed {
					idx = IndexByResource(graph)
				}
				got := SynthesizeManagedBy(obj, "ConfigMap", "app", "config", graph, nil, idx)
				if len(got) != 1 || got[0].Kind != tc.kind || got[0].Group != tc.group || got[0].Name != tc.manager {
					t.Fatalf("indexed=%v manager=%+v", indexed, got)
				}
				rel := GetRelationshipsWithObject("ConfigMap", "app", "config", obj, graph, nil, nil, idx)
				if rel == nil || len(rel.ManagedBy) != 1 || rel.ManagedBy[0].Name != tc.manager || rel.ManagedBy[0].Group != tc.group {
					t.Fatalf("indexed=%v relationship=%+v", indexed, rel)
				}
			}
		})
	}
}
