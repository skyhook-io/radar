package topology

import (
	"fmt"
	"sort"

	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func addFluxSourceEdges(nodes []Node, edges []Edge, roots []*unstructured.Unstructured, provider DynamicProvider, opts BuildOptions) ([]Node, []Edge, []string) {
	var refs []declaredDependency
	upstream := map[string]resourceid.Ref{}
	for _, root := range roots {
		for _, ref := range gitops.FluxSourceReferences(root) {
			refs = append(refs, declaredDependency{Source: resourceid.NewRef(resourceid.GroupFromAPIVersion(root.GetAPIVersion()), root.GetKind(), root.GetNamespace(), root.GetName()), Target: ref.Ref, Label: ref.Role, Configuration: true})
			if isUpstreamFluxSource(ref.Ref) {
				upstream[ref.Key()] = ref.Ref
			}
		}
	}
	nodes, edges, warnings := addObservedDependencyEdges(nodes, edges, refs, provider, opts)
	if provider == nil {
		return nodes, edges, warnings
	}
	// Follow observed upstream sources (a chart to its source, an artifact to
	// its generator, a generator to its sources) a few levels at most. Each
	// kind is one cached list, shared by every reference, never a per-object
	// Get, and only objects already in the graph are followed.
	listed := map[resourceid.GroupKind][]*unstructured.Unstructured{}
	followed := map[string]bool{}
	for level := 0; level < maxFluxSourceLevels && len(upstream) > 0; level++ {
		present := map[string]bool{}
		for i := range nodes {
			for _, key := range nodeResourceKeys(&nodes[i]) {
				present[key] = true
			}
		}
		byKind := map[resourceid.GroupKind]bool{}
		for key, ref := range upstream {
			if present[key] && !followed[key] {
				byKind[ref.GroupKind()] = true
			}
		}
		kinds := make([]resourceid.GroupKind, 0, len(byKind))
		for kind := range byKind {
			kinds = append(kinds, kind)
		}
		sort.Slice(kinds, func(i, j int) bool { return kinds[i].String() < kinds[j].String() })
		refs = nil
		next := map[string]resourceid.Ref{}
		for _, kind := range kinds {
			objects, ok := listed[kind]
			if !ok {
				gvr, found := provider.GetGVRWithGroup(kind.Kind, kind.Group)
				if !found {
					listed[kind] = nil
					continue
				}
				var err error
				objects, err = provider.ListNamespaces(gvr, opts.Namespaces)
				if err != nil {
					warnings = append(warnings, fmt.Sprintf("Failed to list Flux sources %s: %v", kind.String(), err))
				}
				listed[kind] = objects
			}
			for _, obj := range objects {
				if obj == nil {
					continue
				}
				self := resourceid.NewRef(kind.Group, kind.Kind, obj.GetNamespace(), obj.GetName())
				if _, wanted := upstream[self.Key()]; !wanted || !present[self.Key()] || followed[self.Key()] {
					continue
				}
				followed[self.Key()] = true
				for _, ref := range gitops.FluxSourceReferences(obj) {
					refs = append(refs, declaredDependency{Source: self, Target: ref.Ref, Label: ref.Role, Configuration: true})
					if isUpstreamFluxSource(ref.Ref) {
						next[ref.Key()] = ref.Ref
					}
				}
			}
		}
		var sourceWarnings []string
		nodes, edges, sourceWarnings = addObservedDependencyEdges(nodes, edges, refs, provider, opts)
		warnings = append(warnings, sourceWarnings...)
		upstream = next
	}
	return nodes, edges, warnings
}

// maxFluxSourceLevels bounds the walk up a Flux source chain from what the
// roots reference (ExternalArtifact → ArtifactGenerator → its sources).
const maxFluxSourceLevels = 3

// isUpstreamFluxSource: a source that itself names further sources.
func isUpstreamFluxSource(ref resourceid.Ref) bool {
	switch ref.Group {
	case "source.toolkit.fluxcd.io":
		return ref.Kind == "HelmChart" || ref.Kind == "ExternalArtifact"
	case "source.extensions.fluxcd.io":
		return ref.Kind == "ArtifactGenerator"
	}
	return false
}
