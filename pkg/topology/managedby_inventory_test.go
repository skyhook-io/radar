package topology

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"testing"
)

func TestObservedInventoryConstrainHintsWithoutLosingDescendants(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		authoritative, managed, expected bool
	}{
		{"inventory includes parent", true, true, true},
		{"inventory excludes parent", true, false, false},
		{"controller without inventory", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			topo := &Topology{Nodes: []Node{
				{ID: "helmrelease/flux/release", Kind: KindHelmRelease, Name: "release", gitopsManagementAuthoritative: tc.authoritative, Data: map[string]any{"namespace": "flux", "apiVersion": "helm.toolkit.fluxcd.io/v2"}},
				{ID: "deployment/app/web", Kind: KindDeployment, Name: "web", Data: map[string]any{"namespace": "app"}},
				{ID: "pod/app/generated", Kind: KindPod, Name: "generated", Data: map[string]any{"namespace": "app"}},
			}, Edges: []Edge{{Source: "deployment/app/web", Target: "pod/app/generated", Type: EdgeManages}}}
			if tc.managed {
				topo.Edges = append(topo.Edges, Edge{Source: "helmrelease/flux/release", Target: "deployment/app/web", Type: EdgeManages})
			}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "generated", Labels: map[string]string{fluxHelmNameLabel: "release", fluxHelmNSLabel: "flux"}}}
			for _, indexed := range []bool{false, true} {
				var idx *RelationshipsIndex
				if indexed {
					idx = IndexByResource(topo)
				}
				got := SynthesizeManagedBy(pod, "Pod", "app", "generated", topo, nil, idx)
				helm := len(got) > 0 && got[0].Group == fluxHelmGroup
				if helm != tc.expected {
					t.Fatalf("indexed=%v manager=%+v", indexed, got)
				}
			}
		})
	}
}

func TestNestedFluxBuilderKeepsNearestManager(t *testing.T) {
	for _, helm := range []bool{false, true} {
		for _, staleLabel := range []bool{false, true} {
			t.Run(map[bool]string{false: "kustomization", true: "helm"}[helm]+"/"+map[bool]string{false: "unlabelled", true: "stale root label"}[staleLabel], func(t *testing.T) {
				ksGVR := schema.GroupVersionResource{Group: fluxKustomizeGroup, Version: "v1", Resource: "kustomizations"}
				hrGVR := schema.GroupVersionResource{Group: fluxHelmGroup, Version: "v2", Resource: "helmreleases"}
				repoGVR := schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}
				root := genericIdentityObject(ksGVR, "Kustomization", "flux-system", "flux-system")
				apps := genericIdentityObject(ksGVR, "Kustomization", "flux-system", "apps")
				repo := genericIdentityObject(repoGVR, "GitRepository", "flux-system", "flux-system")
				inventory := func(obj *unstructured.Unstructured, ids ...string) {
					entries := []any{}
					for _, id := range ids {
						entries = append(entries, map[string]any{"id": id})
					}
					obj.Object["status"] = map[string]any{"inventory": map[string]any{"entries": entries}}
				}
				for _, ks := range []*unstructured.Unstructured{root, apps} {
					ks.Object["spec"] = map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "flux-system"}}
				}
				inventory(root, "flux-system_flux-system_source.toolkit.fluxcd.io_GitRepository", "flux-system_apps_kustomize.toolkit.fluxcd.io_Kustomization")
				inventory(apps, "prod_web_apps_Deployment")
				resources := map[schema.GroupVersionResource][]*unstructured.Unstructured{ksGVR: {root, apps}, repoGVR: {repo}}
				kinds := map[schema.GroupVersionResource]string{ksGVR: "Kustomization", repoGVR: "GitRepository"}
				watched := []schema.GroupVersionResource{ksGVR, repoGVR}
				wantKind, wantName, wantGroup := "Kustomization", "apps", fluxKustomizeGroup
				if helm {
					hr := genericIdentityObject(hrGVR, "HelmRelease", "prod", "podinfo")
					inventory(hr, "prod_web_apps_Deployment")
					inventory(apps, "prod_podinfo_helm.toolkit.fluxcd.io_HelmRelease")
					resources[hrGVR] = []*unstructured.Unstructured{hr}
					kinds[hrGVR] = "HelmRelease"
					watched = append(watched, hrGVR)
					wantKind, wantName, wantGroup = "HelmRelease", "podinfo", fluxHelmGroup
				}
				dp := &genericIdentityDynamic{watched: watched, kinds: kinds, resources: resources, listCalls: map[schema.GroupVersionResource]int{}}
				ctrl := true
				owner := func(kind, name string) []metav1.OwnerReference {
					return []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: kind, Name: name, Controller: &ctrl}}
				}
				pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "web-rs-pod", OwnerReferences: owner("ReplicaSet", "web-rs")}}
				if staleLabel {
					pod.Labels = map[string]string{fluxKustomizeNameLabel: "flux-system", fluxKustomizeNSLabel: "flux-system"}
				}
				provider := &mockProvider{deployments: []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "web"}}}, replicaSets: []*appsv1.ReplicaSet{{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "web-rs", OwnerReferences: owner("Deployment", "web")}}}, pods: []*corev1.Pod{pod}}
				opts := DefaultBuildOptions()
				opts.IncludeReplicaSets = true
				topo, err := NewBuilder(provider).WithDynamic(dp).Build(opts)
				if err != nil {
					t.Fatal(err)
				}
				rootID := "kustomization/flux-system/flux-system"
				repoID := "gitrepository/flux-system/flux-system"
				for _, edge := range topo.Edges {
					if edge.Source == repoID && edge.Target == rootID && edge.Type == EdgeManages {
						t.Fatal("source dependency formed a management cycle")
					}
				}
				rootRel := GetRelationshipsWithObject("Kustomization", "flux-system", "flux-system", root, topo, provider, dp, IndexByResource(topo))
				if rootRel == nil || rootRel.Owner != nil {
					t.Fatalf("repository incorrectly owns the reconciler: %+v", rootRel)
				}
				ownedRepo, usedRepo := false, false
				for _, ref := range rootRel.Children {
					ownedRepo = ownedRepo || ref.Kind == "GitRepository" && ref.Name == "flux-system"
				}
				for _, ref := range rootRel.Dependencies {
					usedRepo = usedRepo || ref.Kind == "GitRepository" && ref.Name == "flux-system"
				}
				if !ownedRepo || !usedRepo {
					t.Fatalf("bootstrap inventory ownership or source dependency lost: %+v", rootRel)
				}
				rel := GetRelationshipsWithObject("Pod", "prod", pod.Name, pod, topo, provider, dp, IndexByResource(topo))
				if rel == nil || len(rel.ManagedBy) != 1 || rel.ManagedBy[0].Kind != wantKind || rel.ManagedBy[0].Name != wantName || rel.ManagedBy[0].Group != wantGroup {
					t.Fatalf("managedBy=%+v want %s/%s (%s)", rel, wantKind, wantName, wantGroup)
				}
			})
		}
	}
}
