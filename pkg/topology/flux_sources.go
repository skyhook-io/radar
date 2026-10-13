package topology

import (
	"fmt"

	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func addFluxSourceEdges(nodes []Node, edges []Edge, roots []*unstructured.Unstructured, provider DynamicProvider, opts BuildOptions) ([]Node, []Edge, []string) {
	var refs []declaredDependency
	chartTargets := map[string]bool{}
	for _, root := range roots {
		for _, ref := range gitops.FluxSourceReferences(root) {
			refs = append(refs, declaredDependency{Source: resourceid.NewRef(resourceid.GroupFromAPIVersion(root.GetAPIVersion()), root.GetKind(), root.GetNamespace(), root.GetName()), Target: ref.Ref, Label: ref.Role})
			if ref.Kind == "HelmChart" {
				chartTargets[ref.Key()] = true
			}
		}
	}
	nodes, edges, warnings := addObservedDependencyEdges(nodes, edges, refs, provider, opts)
	observedCharts := map[string]bool{}
	for i := range nodes {
		for _, key := range nodeResourceKeys(&nodes[i]) {
			if chartTargets[key] {
				observedCharts[key] = true
			}
		}
	}
	if len(observedCharts) == 0 || provider == nil {
		return nodes, edges, warnings
	}
	// Follow the source of observed referenced charts. This is a second cached
	// kind read, shared by all chart references, never a per-chart Get.
	gvr, found := provider.GetGVRWithGroup("HelmChart", "source.toolkit.fluxcd.io")
	if !found {
		return nodes, edges, warnings
	}
	charts, err := provider.ListNamespaces(gvr, opts.Namespaces)
	if err != nil {
		return nodes, edges, append(warnings, fmt.Sprintf("Failed to list Flux chart sources: %v", err))
	}
	refs = nil
	for _, chart := range charts {
		if chart == nil || !observedCharts[resourceid.ResourceKey(gvr.Group, "HelmChart", chart.GetNamespace(), chart.GetName())] {
			continue
		}
		for _, ref := range gitops.FluxSourceReferences(chart) {
			refs = append(refs, declaredDependency{Source: resourceid.NewRef(gvr.Group, "HelmChart", chart.GetNamespace(), chart.GetName()), Target: ref.Ref, Label: ref.Role})
		}
	}
	nodes, edges, sourceWarnings := addObservedDependencyEdges(nodes, edges, refs, provider, opts)
	return nodes, edges, append(warnings, sourceWarnings...)
}
