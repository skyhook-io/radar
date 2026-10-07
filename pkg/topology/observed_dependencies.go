package topology

import (
	"fmt"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"sort"
	"strings"
)

// declaredDependency comes from a controller field with known group and scope.
// Targets are materialized only when present in the dynamic cache.
type declaredDependency struct {
	Source, Target resourceid.Ref
	Label          string
}

func addObservedDependencyEdges(nodes []Node, edges []Edge, refs []declaredDependency, provider DynamicProvider, opts BuildOptions) ([]Node, []Edge, []string) {
	if provider == nil || len(refs) == 0 {
		return nodes, edges, nil
	}
	byResource := make(map[string]string, len(nodes))
	for i := range nodes {
		for _, key := range nodeResourceKeys(&nodes[i]) {
			byResource[key] = nodes[i].ID
		}
	}
	byKind := map[resourceid.GroupKind][]declaredDependency{}
	for _, ref := range refs {
		if ref.Target.Name == "" || ref.Target.Kind == "" || byResource[ref.Source.Key()] == "" {
			continue
		}
		if ref.Source.Namespace != "" && !opts.MatchesNamespaceFilter(ref.Source.Namespace) || ref.Target.Namespace != "" && !opts.MatchesNamespaceFilter(ref.Target.Namespace) {
			continue
		}
		key := ref.Target.GroupKind()
		byKind[key] = append(byKind[key], ref)
	}
	kinds := make([]resourceid.GroupKind, 0, len(byKind))
	for key := range byKind {
		kinds = append(kinds, key)
	}
	sort.Slice(kinds, func(i, j int) bool {
		if kinds[i].Group != kinds[j].Group {
			return kinds[i].Group < kinds[j].Group
		}
		return kinds[i].Kind < kinds[j].Kind
	})
	seenEdges := map[[2]string]bool{}
	for _, edge := range edges {
		if edge.Type == EdgeUses {
			seenEdges[[2]string{edge.Source, edge.Target}] = true
		}
	}
	var warnings []string
	for _, kind := range kinds {
		gvr, ok := provider.GetGVRWithGroup(kind.Kind, kind.Group)
		if !ok {
			continue
		}
		dependencies := byKind[kind]
		namespaces := opts.Namespaces
		for _, ref := range dependencies {
			if ref.Target.Namespace == "" {
				namespaces = nil
				break
			}
		}
		objects, err := provider.ListNamespaces(gvr, namespaces)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Failed to list dependency targets %s: %v", kind.String(), err))
			continue
		}
		observed := map[string]int{}
		for i, obj := range objects {
			if obj == nil || obj.GetNamespace() != "" && !opts.MatchesNamespaceFilter(obj.GetNamespace()) {
				continue
			}
			observed[resourceid.ResourceKey(gvr.Group, kind.Kind, obj.GetNamespace(), obj.GetName())] = i
		}
		for _, ref := range dependencies {
			index, exists := observed[ref.Target.Key()]
			if !exists {
				continue
			}
			obj := objects[index]
			source := byResource[ref.Source.Key()]
			target := byResource[ref.Target.Key()]
			if target == "" {
				target = fmt.Sprintf("%s/%s/%s/%s", strings.ToLower(kind.Kind), obj.GetNamespace(), obj.GetName(), gvr.Group)
				data := map[string]any{"namespace": obj.GetNamespace(), "labels": obj.GetLabels(), "apiVersion": obj.GetAPIVersion()}
				if obj.GetNamespace() == "" {
					data[clusterScopedGroupKey] = gvr.Group
					data[clusterScopedResourceKey] = gvr.Resource
				}
				nodes = append(nodes, Node{uid: obj.GetUID(), ID: target, Kind: NodeKind(kind.Kind), Name: obj.GetName(), Status: extractGenericStatus(obj), Data: data})
				byResource[ref.Target.Key()] = target
			}
			pair := [2]string{source, target}
			if seenEdges[pair] {
				continue
			}
			edges = append(edges, Edge{ID: source + "-to-" + target, Source: source, Target: target, Type: EdgeUses, Label: ref.Label})
			seenEdges[pair] = true
		}
	}
	return nodes, edges, warnings
}
