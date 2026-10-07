package topology

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestFluxSourceGraphFollowsReferencedChart(t *testing.T) {
	makeObject := func(group, kind, namespace, name string, spec map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": group + "/v1", "kind": kind, "metadata": map[string]any{"namespace": namespace, "name": name}, "spec": spec}}
	}
	hr := makeObject("helm.toolkit.fluxcd.io", "HelmRelease", "team", "app", map[string]any{"chartRef": map[string]any{"kind": "HelmChart", "name": "chart", "namespace": "shared"}})
	ks := makeObject("kustomize.toolkit.fluxcd.io", "Kustomization", "team", "config", map[string]any{"sourceRef": map[string]any{"kind": "Bucket", "name": "files"}})
	chart := makeObject("source.toolkit.fluxcd.io", "HelmChart", "shared", "chart", map[string]any{"sourceRef": map[string]any{"kind": "HelmRepository", "name": "repo"}})
	unused := makeObject("source.toolkit.fluxcd.io", "HelmChart", "shared", "unused", map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "do-not-look-up"}})
	gvrs := map[string]schema.GroupVersionResource{}
	for kind, resource := range map[string]string{"HelmChart": "helmcharts", "HelmRepository": "helmrepositories", "Bucket": "buckets", "GitRepository": "gitrepositories"} {
		gvrs[kind] = schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: resource}
	}
	p := &issuerTestProvider{monitorDynamicProvider: monitorDynamicProvider{gvrs: gvrs, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{
		gvrs["HelmChart"]: {chart, unused}, gvrs["HelmRepository"]: {makeObject("source.toolkit.fluxcd.io", "HelmRepository", "shared", "repo", nil)}, gvrs["Bucket"]: {makeObject("source.toolkit.fluxcd.io", "Bucket", "team", "files", nil)},
	}}, listCalls: map[string]int{}}
	nodes := []Node{{ID: "helmrelease/team/app", Kind: KindHelmRelease, Name: "app", Data: map[string]any{"namespace": "team", "apiVersion": hr.GetAPIVersion()}}, {ID: "kustomization/team/config", Kind: KindKustomization, Name: "config", Data: map[string]any{"namespace": "team", "apiVersion": ks.GetAPIVersion()}}}
	nodes, edges, warnings := addFluxSourceEdges(nodes, nil, []*unstructured.Unstructured{hr, ks}, p, DefaultBuildOptions())
	if len(nodes) != 5 || len(edges) != 3 || len(warnings) != 0 || p.getCalls != 0 || p.listCalls["helmcharts"] != 2 || p.listCalls["gitrepositories"] != 0 {
		t.Fatalf("source chain: nodes=%+v edges=%+v warnings=%v lists=%v gets=%d", nodes, edges, warnings, p.listCalls, p.getCalls)
	}
	rel := GetRelationships("HelmRelease", "team", "app", &Topology{Nodes: nodes, Edges: edges}, nil, p)
	if rel == nil || rel.Owner != nil || len(rel.Dependencies) != 1 || rel.Dependencies[0].Kind != "HelmChart" || rel.Dependencies[0].Namespace != "shared" {
		t.Fatalf("source presented as owner: %+v", rel)
	}
	chartRel := GetRelationships("HelmChart", "shared", "chart", &Topology{Nodes: nodes, Edges: edges}, nil, p)
	if chartRel == nil || len(chartRel.Dependencies) != 1 || len(chartRel.Dependents) != 1 {
		t.Fatalf("chart traversal: %+v", chartRel)
	}
	if len(rel.ConfigRefs) != 1 || rel.ConfigRefs[0] != rel.Dependencies[0] || len(chartRel.Consumers) != 1 || chartRel.Consumers[0] != chartRel.Dependents[0] {
		t.Fatalf("versioned Hub navigation lost: release=%+v chart=%+v", rel, chartRel)
	}
	opts := DefaultBuildOptions()
	opts.Namespaces = []string{"team"}
	nodes, edges, _ = addFluxSourceEdges(nodes[:2], nil, []*unstructured.Unstructured{hr, ks}, p, opts)
	if len(nodes) != 3 || len(edges) != 1 {
		t.Fatalf("cross-namespace source leaked through namespace filter: nodes=%+v edges=%+v", nodes, edges)
	}
}
