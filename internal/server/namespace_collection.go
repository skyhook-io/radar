package server

import (
	"slices"
	"strings"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

// NamespaceCollection is where Radar keeps the built-in kinds it collects in
// only some namespaces. Absent when no kind is narrowed.
type NamespaceCollection struct {
	Groups []NamespaceCollectionGroup `json:"groups"`
	// MayBeIncomplete: the namespace list was cut short, so Radar may collect in more than the groups name.
	MayBeIncomplete bool `json:"mayBeIncomplete,omitempty"`
}

// NamespaceCollectionGroup is the kinds collected in one and the same set of
// namespaces the caller can see.
type NamespaceCollectionGroup struct {
	Kinds      []resourceapi.KindRef `json:"kinds"`
	Namespaces *resourceapi.NameSet  `json:"namespaces"`
}

// namespaceCollectionFor plans each kind as resource counts do, so the picker
// can't disagree with them. A namespace the caller can't see is neither named
// nor counted.
func namespaceCollectionFor(cache *k8s.ResourceCache, permissions *k8s.PermissionCheckResult, visible []string) *NamespaceCollection {
	if cache == nil {
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
		namespaces := slices.DeleteFunc(slices.Clone(plan.collected), func(ns string) bool { return !canSee[ns] })
		key := strings.Join(namespaces, "\x00")
		g := bySet[key]
		if g == nil {
			g = &group{namespaces: namespaces}
			bySet[key] = g
			groups = append(groups, g)
		}
		g.kinds = append(g.kinds, resourceapi.KindRef{Kind: kl.Kind(), Group: kl.Group()})
	}
	if len(groups) == 0 {
		return nil
	}

	out := &NamespaceCollection{
		Groups:          make([]NamespaceCollectionGroup, 0, len(groups)),
		MayBeIncomplete: permissions != nil && permissions.ScopeCandidatesTruncated,
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
