package topology

import (
	"fmt"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestFluxSourceGraphFollowsReferencedChart(t *testing.T) {
	for _, inline := range []bool{false, true} {
		t.Run(fmt.Sprintf("inline=%t", inline), func(t *testing.T) {
			makeObject := func(group, kind, namespace, name string, spec map[string]any) *unstructured.Unstructured {
				return &unstructured.Unstructured{Object: map[string]any{"apiVersion": group + "/v1", "kind": kind, "metadata": map[string]any{"namespace": namespace, "name": name}, "spec": spec}}
			}
			hr := makeObject("helm.toolkit.fluxcd.io", "HelmRelease", "team", "app", map[string]any{"chartRef": map[string]any{"kind": "HelmChart", "name": "chart", "namespace": "shared"}})
			if inline {
				hr.Object["spec"] = map[string]any{"chart": map[string]any{"spec": map[string]any{"sourceRef": map[string]any{"kind": "HelmRepository", "name": "repo", "namespace": "shared"}}}}
				hr.Object["status"] = map[string]any{"helmChart": "shared/chart"}
			}
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
			wantEdges := 3
			if inline {
				wantEdges++
			}
			if len(nodes) != 5 || len(edges) != wantEdges || len(warnings) != 0 || p.getCalls != 0 || p.listCalls["helmcharts"] != 2 || p.listCalls["gitrepositories"] != 0 {
				t.Fatalf("source chain: nodes=%+v edges=%+v warnings=%v lists=%v gets=%d", nodes, edges, warnings, p.listCalls, p.getCalls)
			}

			previousEdges := make([]Edge, len(edges))
			for i, edge := range edges {
				if edge.Type != EdgeConfigures {
					t.Fatalf("source edge must configure upstream: %+v", edge)
				}
				previousEdges[i] = Edge{Source: edge.Target, Target: edge.Source, Type: EdgeUses, Label: edge.Label}
			}
			for _, node := range nodes {
				namespace, _ := node.Data["namespace"].(string)
				before := GetRelationships(string(node.Kind), namespace, node.Name, &Topology{Nodes: nodes, Edges: previousEdges}, nil, p)
				after := GetRelationships(string(node.Kind), namespace, node.Name, &Topology{Nodes: nodes, Edges: edges}, nil, p)
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("%s %s/%s relationship projection changed:\nbefore=%+v\nafter=%+v", node.Kind, namespace, node.Name, before, after)
				}
			}
			rel := GetRelationships("HelmRelease", "team", "app", &Topology{Nodes: nodes, Edges: edges}, nil, p)
			wantDependencies := 1
			if inline {
				wantDependencies++
			}
			if rel == nil || rel.Owner != nil || len(rel.Dependencies) != wantDependencies || rel.Dependencies[0].Kind != "HelmChart" || rel.Dependencies[0].Namespace != "shared" {
				t.Fatalf("source presented as owner: %+v", rel)
			}
			chartRel := GetRelationships("HelmChart", "shared", "chart", &Topology{Nodes: nodes, Edges: edges}, nil, p)
			if chartRel == nil || len(chartRel.Dependencies) != 1 || len(chartRel.Dependents) != 1 {
				t.Fatalf("chart traversal: %+v", chartRel)
			}
			if len(rel.ConfigRefs) != wantDependencies || rel.ConfigRefs[0] != rel.Dependencies[0] || len(chartRel.Consumers) != 1 || chartRel.Consumers[0] != chartRel.Dependents[0] {
				t.Fatalf("versioned Hub navigation lost: release=%+v chart=%+v", rel, chartRel)
			}
			if inline {
				missingChart := *p
				missingChart.resources = map[schema.GroupVersionResource][]*unstructured.Unstructured{gvrs["HelmRepository"]: p.resources[gvrs["HelmRepository"]]}
				missingNodes, missingEdges, _ := addFluxSourceEdges(nodes[:2], nil, []*unstructured.Unstructured{hr}, &missingChart, DefaultBuildOptions())
				if len(missingNodes) != 3 || len(missingEdges) != 1 {
					t.Fatalf("uncached chart lost declared source: nodes=%+v edges=%+v", missingNodes, missingEdges)
				}
			}

			opts := DefaultBuildOptions()
			opts.Namespaces = []string{"team"}
			nodes, edges, _ = addFluxSourceEdges(nodes[:2], nil, []*unstructured.Unstructured{hr, ks}, p, opts)
			if len(nodes) != 3 || len(edges) != 1 {
				t.Fatalf("cross-namespace source leaked through namespace filter: nodes=%+v edges=%+v", nodes, edges)
			}
		})
	}
}

func TestFluxSourceGraphFollowsArtifactChain(t *testing.T) {
	obj := func(apiVersion, kind, name string, spec map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"namespace": "team", "name": name}, "spec": spec}}
	}
	genRef := map[string]any{"apiVersion": "source.extensions.fluxcd.io/v1beta1", "kind": "ArtifactGenerator", "name": "gen"}
	hr := obj("helm.toolkit.fluxcd.io/v2", "HelmRelease", "app", map[string]any{"chartRef": map[string]any{"kind": "ExternalArtifact", "name": "app"}})
	ea := obj("source.toolkit.fluxcd.io/v1", "ExternalArtifact", "app", map[string]any{"sourceRef": genRef})
	sibling := obj("source.toolkit.fluxcd.io/v1", "ExternalArtifact", "other", map[string]any{"sourceRef": genRef})
	gen := obj("source.extensions.fluxcd.io/v1beta1", "ArtifactGenerator", "gen", map[string]any{"sources": []any{map[string]any{"alias": "repo", "kind": "GitRepository", "name": "platform"}}})
	repo := obj("source.toolkit.fluxcd.io/v1", "GitRepository", "platform", nil)
	gvrs := map[string]schema.GroupVersionResource{
		"ExternalArtifact":  {Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "externalartifacts"},
		"ArtifactGenerator": {Group: "source.extensions.fluxcd.io", Version: "v1beta1", Resource: "artifactgenerators"},
		"GitRepository":     {Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"},
	}
	p := &issuerTestProvider{monitorDynamicProvider: monitorDynamicProvider{gvrs: gvrs, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{
		gvrs["ExternalArtifact"]: {ea, sibling}, gvrs["ArtifactGenerator"]: {gen}, gvrs["GitRepository"]: {repo},
	}}, listCalls: map[string]int{}}
	nodes := []Node{{ID: "helmrelease/team/app", Kind: KindHelmRelease, Name: "app", Data: map[string]any{"namespace": "team", "apiVersion": hr.GetAPIVersion()}}}
	nodes, edges, warnings := addFluxSourceEdges(nodes, nil, []*unstructured.Unstructured{hr}, p, DefaultBuildOptions())
	if len(warnings) != 0 || len(nodes) != 4 || len(edges) != 3 {
		t.Fatalf("chain: nodes=%+v edges=%+v warnings=%v", nodes, edges, warnings)
	}
	for _, n := range nodes {
		if n.Name == "other" {
			t.Fatalf("a sibling artifact joined the graph: %+v", n)
		}
	}
	for _, e := range edges {
		if e.Type != EdgeConfigures {
			t.Fatalf("source edge must configure: %+v", e)
		}
	}
	for resource, n := range p.listCalls {
		if n > 2 {
			t.Fatalf("%s listed %d times: %v", resource, n, p.listCalls)
		}
	}
	rel := GetRelationships("ArtifactGenerator", "team", "gen", &Topology{Nodes: nodes, Edges: edges}, nil, p)
	if rel == nil || len(rel.Dependents) != 1 || rel.Dependents[0].Kind != "ExternalArtifact" || len(rel.Dependencies) != 1 || rel.Dependencies[0].Kind != "GitRepository" {
		t.Fatalf("generator relationships: %+v", rel)
	}
}
