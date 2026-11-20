package server

import (
	"slices"
	"strings"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readplan"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// NamespaceCollection is where Radar keeps the built-in kinds it collects in
// only some namespaces. Absent when no kind is narrowed.
type NamespaceCollection struct {
	Groups []NamespaceCollectionGroup `json:"groups"`
	// MayBeIncomplete: Radar may collect in namespaces it can't name now, so in more than the groups name.
	MayBeIncomplete bool `json:"mayBeIncomplete,omitempty"`
}

// NamespaceCollectionGroup is the kinds collected in one and the same set of
// namespaces the caller can see.
type NamespaceCollectionGroup struct {
	Kinds      []resourceapi.KindRef `json:"kinds"`
	Namespaces *resourceapi.NameSet  `json:"namespaces"`
}

// collectedKind is a kind Radar collects in only the listed namespaces.
type collectedKind struct {
	ref        resourceapi.KindRef
	namespaces []string
}

// dynamicBuiltinKinds picks the namespaced built-in kinds Radar watches
// through its dynamic cache (DRA's ResourceClaims, for example), at their most
// stable served version. Resource counts plan these with the custom resources,
// so they narrow counts the same way.
func dynamicBuiltinKinds(resources []k8score.APIResource) []k8score.APIResource {
	var out []k8score.APIResource
	at := map[string]int{}
	for _, res := range resources {
		if !res.IsCRD || !res.Namespaced || !k8score.IsBuiltInAPIGroup(res.Group) ||
			!slices.Contains(res.Verbs, "list") || !slices.Contains(res.Verbs, "watch") {
			continue
		}
		key := res.Group + "/" + res.Kind
		if i, seen := at[key]; !seen {
			at[key] = len(out)
			out = append(out, res)
		} else if k8score.IsMoreStableVersion(res.Version, out[i].Version) {
			out[i] = res
		}
	}
	return out
}

func gvrOf(res k8score.APIResource) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: res.Group, Version: res.Version, Resource: res.Name}
}

// collectedDynamicKinds plans each kind as resource counts do for an
// all-namespace read.
func collectedDynamicKinds(kinds []k8score.APIResource, observations readplan.NamespaceObserver) []collectedKind {
	var out []collectedKind
	for _, res := range kinds {
		inventory := readplan.ForInventory(readplan.Dynamic(observations, gvrOf(res), nil, false), nil, false, false)
		if inventory.Collection == nil {
			continue
		}
		out = append(out, collectedKind{ref: resourceapi.KindRef{Kind: res.Kind, Group: res.Group}, namespaces: inventory.Collection.Namespaces})
	}
	return out
}

// dynamicBuiltinCollection is collectedDynamicKinds over the live discovery
// and dynamic cache; nil while either is missing.
func dynamicBuiltinCollection() []collectedKind {
	discovery, dynamic := k8s.GetResourceDiscovery(), k8s.GetDynamicResourceCache()
	if discovery == nil || dynamic == nil {
		return nil
	}
	resources, err := discovery.GetAPIResources()
	if err != nil {
		return nil
	}
	kinds := dynamicBuiltinKinds(resources)
	gvrs := make([]schema.GroupVersionResource, 0, len(kinds))
	for _, res := range kinds {
		gvrs = append(gvrs, gvrOf(res))
	}
	return collectedDynamicKinds(kinds, dynamic.NewObservationView(gvrs))
}

// namespaceCollectionFor plans each typed kind as resource counts do, so the
// picker can't disagree with them, and adds dynamic, the built-in kinds Radar
// watches through its dynamic cache. A namespace the caller can't see is
// neither named nor counted. radarList is Radar's own namespace list when it
// is a short one (nil when complete): collecting outside it means Radar may
// collect in more than it can name.
func namespaceCollectionFor(cache *k8s.ResourceCache, permissions *k8s.PermissionCheckResult, dynamic []collectedKind, visible, radarList []string) *NamespaceCollection {
	if cache == nil {
		return nil
	}
	kinds := slices.Clone(dynamic)
	for _, kl := range k8score.AllKindListers() {
		if cache.KindReadinessForKindName(kl.Kind()) == k8score.KindUnavailable {
			continue
		}
		if clusterScoped, _, _ := k8s.ClassifyKindScope(kl.Kind(), kl.Group()); clusterScoped {
			continue
		}
		// The plan carries what Radar collects even when it can't read the kind yet.
		plan, _ := typedReadCoverageFrom(cache, permissions, informerKeyForKind(kl.Kind()), nil, false, false)
		if plan.collected == nil {
			continue
		}
		kinds = append(kinds, collectedKind{ref: resourceapi.KindRef{Kind: kl.Kind(), Group: kl.Group()}, namespaces: plan.collected})
	}
	if len(kinds) == 0 {
		return nil
	}

	canSee := make(map[string]bool, len(visible))
	for _, ns := range visible {
		canSee[ns] = true
	}
	type group struct {
		namespaces []string
		kinds      []resourceapi.KindRef
	}
	var groups []*group
	bySet := map[string]*group{}
	beyondList := false
	for _, k := range kinds {
		if radarList != nil && slices.ContainsFunc(k.namespaces, func(ns string) bool { return !slices.Contains(radarList, ns) }) {
			beyondList = true
		}
		namespaces := slices.DeleteFunc(slices.Clone(k.namespaces), func(ns string) bool { return !canSee[ns] })
		key := strings.Join(namespaces, "\x00")
		g := bySet[key]
		if g == nil {
			g = &group{namespaces: namespaces}
			bySet[key] = g
			groups = append(groups, g)
		}
		g.kinds = append(g.kinds, k.ref)
	}

	out := &NamespaceCollection{
		Groups:          make([]NamespaceCollectionGroup, 0, len(groups)),
		MayBeIncomplete: beyondList || (permissions != nil && permissions.ScopeCandidatesTruncated),
	}
	for _, g := range groups {
		slices.SortFunc(g.kinds, func(a, b resourceapi.KindRef) int { return strings.Compare(a.Kind, b.Kind) })
		out.Groups = append(out.Groups, NamespaceCollectionGroup{
			Kinds:      g.kinds,
			Namespaces: resourceapi.Names(g.namespaces, resourceapi.RESTSetLimit),
		})
	}
	slices.SortFunc(out.Groups, func(a, b NamespaceCollectionGroup) int {
		return strings.Compare(a.Kinds[0].Kind, b.Kinds[0].Kind)
	})
	return out
}
