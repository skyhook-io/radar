package topology

import (
	"github.com/skyhook-io/radar/pkg/configrefs"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Resolve event destinations after generic CRDs have been added. Addressable
// CRDs are not a closed set of four kinds; only exact observed nodes qualify.
func addKnativeReferenceEdges(nodes []Node, edges []Edge, triggers, sources, channels []*unstructured.Unstructured) []Edge {
	ids := map[string]string{}
	ambiguous := map[string]bool{}
	for i := range nodes {
		for _, key := range nodeResourceKeys(&nodes[i]) {
			if prior := ids[key]; prior != "" && prior != nodes[i].ID {
				ambiguous[key] = true
			}
			ids[key] = nodes[i].ID
		}
	}
	seen := map[string]bool{}
	for _, edge := range edges {
		seen[edge.ID] = true
	}
	appendResolvedRef := func(source string, ref resourceid.Ref, reverse bool, edgeType EdgeType) {
		if ambiguous[ref.Key()] {
			return
		}
		target := ids[ref.Key()]
		if source == "" || target == "" || source == target {
			return
		}
		if reverse {
			source, target = target, source
		}
		id := source + "-to-" + target
		if !seen[id] {
			seen[id] = true
			edges = append(edges, Edge{ID: id, Source: source, Target: target, Type: edgeType})
		}
	}
	appendDestinations := func(source string, obj *unstructured.Unstructured) {
		for _, d := range configrefs.KnativeEventDestinations(obj) {
			appendResolvedRef(source, d.Ref, false, EdgeExposes)
		}
	}
	for _, trigger := range triggers {
		id := "trigger/" + trigger.GetNamespace() + "/" + trigger.GetName()
		appendDestinations(id, trigger)
	}
	for _, source := range sources {
		// Source node identities are indexed by their API group, not guessed
		// from their plural (ApiServerSource and APIService differ).
		refKey := resourceid.ResourceKey(resourceid.GroupFromAPIVersion(source.GetAPIVersion()), source.GetKind(), source.GetNamespace(), source.GetName())
		appendDestinations(ids[refKey], source)
	}
	for _, channel := range channels {
		id := ids[resourceid.ResourceKey(resourceid.GroupFromAPIVersion(channel.GetAPIVersion()), channel.GetKind(), channel.GetNamespace(), channel.GetName())]
		// Only recorded ownerReferences establish ownership; status.channel
		// alone is a declared backing/exposure reference, not a GC guarantee.
		for _, owner := range channel.GetOwnerReferences() {
			if owner.APIVersion != "" {
				appendResolvedRef(id, resourceid.NewRef(resourceid.GroupFromAPIVersion(owner.APIVersion), owner.Kind, channel.GetNamespace(), owner.Name), true, EdgeManages)
			}
		}
	}
	// Owner references must win over an identical declared status edge,
	// independent of the discovery/list order of Channel and backing objects.
	for _, channel := range channels {
		appendDestinations(ids[resourceid.ResourceKey(resourceid.GroupFromAPIVersion(channel.GetAPIVersion()), channel.GetKind(), channel.GetNamespace(), channel.GetName())], channel)
	}
	return edges
}
