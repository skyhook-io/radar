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
	appendRef := func(source string, obj *unstructured.Unstructured, reverse bool, fields ...string) {
		raw, present, err := unstructured.NestedMap(obj.Object, fields...)
		if err != nil || !present {
			return
		}
		ref, valid := configrefs.KnativeReference(raw, obj.GetNamespace())
		if valid {
			appendResolvedRef(source, ref, reverse, EdgeExposes)
		}
	}
	for _, trigger := range triggers {
		id := "trigger/" + trigger.GetNamespace() + "/" + trigger.GetName()
		appendRef(id, trigger, false, "spec", "subscriber", "ref")
		// brokerRef is authoritative over the older same-namespace broker field.
		if raw, present, _ := unstructured.NestedMap(trigger.Object, "spec", "brokerRef"); present {
			if ref, valid := configrefs.KnativeTriggerBrokerReference(raw); valid {
				appendResolvedRef(id, ref, true, EdgeExposes)
			}
		}
	}
	for _, source := range sources {
		// Source node identities are indexed by their API group, not guessed
		// from their plural (ApiServerSource and APIService differ).
		refKey := resourceid.ResourceKey(resourceid.GroupFromAPIVersion(source.GetAPIVersion()), source.GetKind(), source.GetNamespace(), source.GetName())
		appendRef(ids[refKey], source, false, "spec", "sink", "ref")
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
		appendRef(ids[resourceid.ResourceKey(resourceid.GroupFromAPIVersion(channel.GetAPIVersion()), channel.GetKind(), channel.GetNamespace(), channel.GetName())], channel, false, "status", "channel")
	}
	return edges
}
