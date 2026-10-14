package topology

import (
	"github.com/skyhook-io/radar/pkg/k8score"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestHelmReleaseInventoryOverridesLabelsAndIncludesConfig(t *testing.T) {
	for _, recorded := range []bool{false, true} {
		t.Run(map[bool]string{true: "inventory", false: "recorded empty"}[recorded], func(t *testing.T) {
			gvr := schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}
			hr := genericIdentityObject(gvr, "HelmRelease", "flux", "release")
			entries := []any{}
			if recorded {
				entries = []any{map[string]any{"id": "prod_web_apps_Deployment"}, map[string]any{"id": "prod_settings_core_ConfigMap"}}
			}
			hr.Object["status"] = map[string]any{"inventory": map[string]any{"entries": entries}}
			d := &genericIdentityDynamic{watched: []schema.GroupVersionResource{gvr}, kinds: map[schema.GroupVersionResource]string{gvr: "HelmRelease"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {hr}}, listCalls: map[schema.GroupVersionResource]int{}}
			provider := &mockProvider{deployments: []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "web"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web", EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}}}}}}}}}}, {ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "decoy", Labels: map[string]string{fluxHelmNameLabel: "release", fluxHelmNSLabel: "flux"}}}}, configMaps: []*corev1.ConfigMap{{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "settings"}}}}
			opts := DefaultBuildOptions()
			opts.IncludeConfigMaps = true
			topo, err := NewBuilder(provider).WithDynamic(d).Build(opts)
			if err != nil {
				t.Fatal(err)
			}
			targets := map[string]bool{}
			for _, edge := range topo.Edges {
				if edge.Source == "helmrelease/flux/release" {
					targets[edge.Target] = true
				}
			}
			decoy := provider.deployments[1]
			if manager := SynthesizeManagedBy(decoy, "Deployment", decoy.Namespace, decoy.Name, topo, d, IndexByResource(topo)); len(manager) != 0 {
				t.Fatalf("stale labels override inventory: %+v", manager)
			}
			if targets["deployment/prod/decoy"] || targets["deployment/prod/web"] != recorded || targets["configmap/prod/settings"] != recorded {
				t.Fatalf("targets=%v", targets)
			}
		})
	}
}

func TestInventoryKeepsManagerForObjectsOmittedFromGraph(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}
	hr := genericIdentityObject(gvr, "HelmRelease", "flux", "release")
	hr.Object["status"] = map[string]any{"inventory": map[string]any{"entries": []any{map[string]any{"id": "prod_dashboard__ConfigMap"}, map[string]any{"id": "prod_reader_rbac.authorization.k8s.io_Role"}, map[string]any{"id": "prod_global_rbac.authorization.k8s.io_ClusterRole"}}}}
	d := &genericIdentityDynamic{watched: []schema.GroupVersionResource{gvr}, kinds: map[schema.GroupVersionResource]string{gvr: "HelmRelease"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {hr}}, listCalls: map[schema.GroupVersionResource]int{}}
	topo, err := NewBuilder(&mockProvider{}).WithDynamic(d).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ kind, group, namespace, name string }{{"ConfigMap", "", "prod", "dashboard"}, {"Role", "rbac.authorization.k8s.io", "prod", "reader"}, {"ClusterRole", "rbac.authorization.k8s.io", "", "global"}} {
		obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": tc.group + "/v1", "kind": tc.kind, "metadata": map[string]any{"namespace": tc.namespace, "name": tc.name, "labels": map[string]any{fluxHelmNameLabel: "release", fluxHelmNSLabel: "flux"}}}}
		if tc.group == "" {
			obj.SetAPIVersion("v1")
		}
		rel := GetRelationshipsWithObject(tc.kind, tc.namespace, tc.name, obj, topo, nil, d, IndexByResource(topo))
		if rel == nil || len(rel.ManagedBy) != 1 || rel.ManagedBy[0].Name != "release" {
			t.Fatalf("%s missing manager: %+v", tc.kind, rel)
		}
		obj.SetName("stale")
		rel = GetRelationshipsWithObject(tc.kind, tc.namespace, "stale", obj, topo, nil, d, IndexByResource(topo))
		if rel != nil && len(rel.ManagedBy) > 0 {
			t.Fatal("labels overrode contradictory inventory", rel)
		}
	}
}

type clusterIssuerInventoryDynamic struct{ *genericIdentityDynamic }

func (d *clusterIssuerInventoryDynamic) GetResourceWithGroup(kind, group string) (k8score.APIResource, bool) {
	return k8score.APIResource{Kind: "ClusterIssuer", Group: "cert-manager.io", Namespaced: false}, kind == "ClusterIssuer" && group == "cert-manager.io"
}
func TestInventoryNormalizesDiscoveredClusterCRDOmittedFromGraph(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}
	hr := genericIdentityObject(gvr, "HelmRelease", "flux", "release")
	hr.Object["status"] = map[string]any{"inventory": map[string]any{"entries": []any{map[string]any{"id": "prod_global_cert-manager.io_ClusterIssuer"}}}}
	d := &clusterIssuerInventoryDynamic{&genericIdentityDynamic{watched: []schema.GroupVersionResource{gvr}, kinds: map[schema.GroupVersionResource]string{gvr: "HelmRelease"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {hr}}, listCalls: map[schema.GroupVersionResource]int{}}}
	topo, err := NewBuilder(&mockProvider{}).WithDynamic(d).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "cert-manager.io/v1", "kind": "ClusterIssuer", "metadata": map[string]any{"name": "global", "labels": map[string]any{fluxHelmNameLabel: "release", fluxHelmNSLabel: "flux"}}}}
	refs := SynthesizeManagedBy(obj, "ClusterIssuer", "", "global", topo, d, IndexByResource(topo))
	if len(refs) != 1 || refs[0].Kind != "HelmRelease" || refs[0].Name != "release" {
		t.Fatalf("cluster CRD ownership lost: %+v", refs)
	}
}

func TestHelmHookOriginSurvivesInventoryWithoutMembership(t *testing.T) {
	for _, remote := range []bool{false, true} {
		gvr := schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}
		hr := genericIdentityObject(gvr, "HelmRelease", "flux", "release")
		hr.Object["status"] = map[string]any{"inventory": map[string]any{"entries": []any{}}}
		if remote {
			hr.Object["spec"] = map[string]any{"kubeConfig": map[string]any{"secretRef": map[string]any{"name": "remote"}}}
		}
		d := &genericIdentityDynamic{watched: []schema.GroupVersionResource{gvr}, kinds: map[schema.GroupVersionResource]string{gvr: "HelmRelease"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {hr}}, listCalls: map[schema.GroupVersionResource]int{}}
		topo, err := NewBuilder(&mockProvider{}).WithDynamic(d).Build(DefaultBuildOptions())
		if err != nil {
			t.Fatal(err)
		}
		job := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "batch/v1", "kind": "Job", "metadata": map[string]any{"namespace": "prod", "name": "migration", "annotations": map[string]any{"helm.sh/hook": "pre-upgrade"}, "labels": map[string]any{fluxHelmNameLabel: "release", fluxHelmNSLabel: "flux"}}}}
		refs := SynthesizeManagedBy(job, "Job", "prod", "migration", topo, d, IndexByResource(topo))
		if remote && len(refs) != 0 || !remote && (len(refs) != 1 || refs[0].Name != "release") {
			t.Fatalf("remote=%v refs=%+v", remote, refs)
		}
		for _, edge := range topo.Edges {
			if edge.Source == "helmrelease/flux/release" && edge.Type == EdgeManages {
				t.Fatal("hook attribution added inventory membership")
			}
		}
		job.SetAnnotations(nil)
		if refs := SynthesizeManagedBy(job, "Job", "prod", "migration", topo, d, IndexByResource(topo)); len(refs) != 0 {
			t.Fatal("non-hook labels resurrected empty inventory", refs)
		}
	}
}
