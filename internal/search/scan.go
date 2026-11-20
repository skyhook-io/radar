package search

import (
	"cmp"
	"context"
	"maps"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readscope"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

// ScannedObject is one object a Scan reached. Object is borrowed from the
// cache: read it, don't change or retain it. A kind served both as a typed and
// as a watched dynamic resource is visited once per path.
type ScannedObject struct {
	Kind   string
	Group  string
	Object metav1.Object
}

// ScanAccess is the caller's access for a Scan, with the meanings of the
// Options fields of the same names. Build it as for a Search.
type ScanAccess struct {
	Selection            readscope.Selection
	NamespaceCheckFailed bool
	TypedKindAccess      func(namespaces []string, group, resource string) (allowed, denied, unchecked []string)
	CanReadClusterScoped func(kind, group, resource string) (allowed, authoritative bool)
	DynamicKindAccess    *DynamicKindAccess
}

// Scan walks what a broad Search walks, under the same per-user gates and with
// the same coverage, and passes every object in scope to visit instead of
// scoring it, so it has no result limit. Kinds that skip selects are never
// listed, whatever the caller may read. Like a broad Search, it reads the kinds
// Radar already watches and warms none. Before publishing, the caller must
// verify that its captured provider is still current.
func Scan(ctx context.Context, p Provider, access ScanAccess, skip func(kind, group string) bool, visit func(ScannedObject)) (resourceapi.SearchCoverage, error) {
	res, err := Search(ctx, p, Query{}, Options{
		Selection:            access.Selection,
		NamespaceCheckFailed: access.NamespaceCheckFailed,
		TypedKindAccess:      access.TypedKindAccess,
		CanReadClusterScoped: access.CanReadClusterScoped,
		DynamicKindAccess:    access.DynamicKindAccess,
		skip:                 skip,
		visit:                visit,
	})
	return res.Coverage, err
}

// PermitsObject reports whether the caller's gates let them read an object of
// this kind and API group in namespace, as Scan applies them: namespace
// visibility, the exact-kind check for tightly gated kinds, and the per-kind
// check for cluster-scoped kinds, whose namespace is ignored. It answers for
// the caller only, not for whether Radar collects the kind. When the object
// isn't permitted, reason says why: user_permission_check_failed when a check
// went unanswered, which isn't a denial.
func (a ScanAccess) PermitsObject(kind, group, namespace string) (permitted bool, reason readreason.Code) {
	if clusterScoped, gvrGroup, gvrResource := k8s.ClassifyKindScope(kind, group); clusterScoped {
		if a.CanReadClusterScoped == nil {
			return true, ""
		}
		if allowed, authoritative := a.CanReadClusterScoped(kind, gvrGroup, gvrResource); !allowed {
			return false, deniedReason(authoritative)
		}
		return true, ""
	}
	if a.NamespaceCheckFailed {
		return false, readreason.UserPermissionCheckFailed
	}
	if a.Selection.Namespaces != nil && !slices.Contains(a.Selection.Namespaces, namespace) {
		return false, readreason.UserNamespaceNotVisible
	}
	exactGroup, exactResource := k8s.ExactNamespaceAccessResource(kind, group)
	if exactResource == "" {
		return true, ""
	}
	if exactGroup == trivyGroup {
		if a.DynamicKindAccess == nil {
			return true, ""
		}
		if allowed, _ := a.DynamicKindAccess.ClusterWide(exactGroup, exactResource); allowed {
			return true, ""
		}
		allowed, unchecked := a.DynamicKindAccess.InNamespaces(exactGroup, exactResource, []string{namespace})
		return namespaceDecision(namespace, allowed, unchecked)
	}
	if a.TypedKindAccess == nil {
		return true, ""
	}
	allowed, _, unchecked := a.TypedKindAccess([]string{namespace}, exactGroup, exactResource)
	return namespaceDecision(namespace, allowed, unchecked)
}

func namespaceDecision(namespace string, allowed, unchecked []string) (bool, readreason.Code) {
	switch {
	case slices.Contains(allowed, namespace):
		return true, ""
	case slices.Contains(unchecked, namespace):
		return false, readreason.UserPermissionCheckFailed
	}
	return false, readreason.UserForbidden
}

// ObjectRef is an object's kind, API group and namespace, as PermitsObject
// takes them.
type ObjectRef struct {
	Kind, Group, Namespace string
}

type kindKey struct {
	group, resource string
}

type clusterDecision struct {
	allowed, authoritative bool
}

// typedSplit is one TypedKindAccess answer, by namespace.
type typedSplit struct {
	allowed, denied, unchecked map[string]bool
}

// Resolve makes the checks PermitsObject needs for refs up front, the way
// Search makes them: one cluster-scoped check per kind, and one exact-kind
// check per kind across all of its namespaces at once, so the namespace checks
// run concurrently under the request's budget. The returned access answers
// those refs from the results and passes anything else to a's own gates, so
// PermitsObject answers as it would on a. Kinds gated by DynamicKindAccess
// aren't resolved up front.
func (a ScanAccess) Resolve(refs []ObjectRef) ScanAccess {
	clusterKinds := map[kindKey]string{}
	typedNamespaces := map[kindKey][]string{}
	for _, ref := range refs {
		if clusterScoped, group, resource := k8s.ClassifyKindScope(ref.Kind, ref.Group); clusterScoped {
			clusterKinds[kindKey{group, resource}] = ref.Kind
			continue
		}
		if a.NamespaceCheckFailed || (a.Selection.Namespaces != nil && !slices.Contains(a.Selection.Namespaces, ref.Namespace)) {
			continue
		}
		group, resource := k8s.ExactNamespaceAccessResource(ref.Kind, ref.Group)
		if resource == "" || group == trivyGroup {
			continue
		}
		key := kindKey{group, resource}
		if !slices.Contains(typedNamespaces[key], ref.Namespace) {
			typedNamespaces[key] = append(typedNamespaces[key], ref.Namespace)
		}
	}

	resolved := a
	if a.CanReadClusterScoped != nil && len(clusterKinds) > 0 {
		decisions := make(map[kindKey]clusterDecision, len(clusterKinds))
		for key, kind := range clusterKinds {
			allowed, authoritative := a.CanReadClusterScoped(kind, key.group, key.resource)
			decisions[key] = clusterDecision{allowed, authoritative}
		}
		resolved.CanReadClusterScoped = func(kind, group, resource string) (bool, bool) {
			if d, ok := decisions[kindKey{group, resource}]; ok {
				return d.allowed, d.authoritative
			}
			return a.CanReadClusterScoped(kind, group, resource)
		}
	}
	if a.TypedKindAccess != nil && len(typedNamespaces) > 0 {
		splits := make(map[kindKey]typedSplit, len(typedNamespaces))
		// In a fixed order, so which kinds a spent budget leaves unchecked
		// doesn't vary between requests.
		keys := slices.SortedFunc(maps.Keys(typedNamespaces), func(a, b kindKey) int {
			return cmp.Or(cmp.Compare(a.group, b.group), cmp.Compare(a.resource, b.resource))
		})
		for _, key := range keys {
			namespaces := typedNamespaces[key]
			slices.Sort(namespaces)
			allowed, denied, unchecked := a.TypedKindAccess(namespaces, key.group, key.resource)
			splits[key] = typedSplit{allowed: setOf(allowed), denied: setOf(denied), unchecked: setOf(unchecked)}
		}
		resolved.TypedKindAccess = func(namespaces []string, group, resource string) (allowed, denied, unchecked []string) {
			split, ok := splits[kindKey{group, resource}]
			if !ok || namespaces == nil {
				return a.TypedKindAccess(namespaces, group, resource)
			}
			for _, ns := range namespaces {
				if !split.allowed[ns] && !split.denied[ns] && !split.unchecked[ns] {
					return a.TypedKindAccess(namespaces, group, resource)
				}
			}
			allowed, denied, unchecked = []string{}, []string{}, []string{}
			for _, ns := range namespaces {
				switch {
				case split.allowed[ns]:
					allowed = append(allowed, ns)
				case split.unchecked[ns]:
					unchecked = append(unchecked, ns)
				default:
					denied = append(denied, ns)
				}
			}
			return allowed, denied, unchecked
		}
	}
	return resolved
}

func setOf(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, v := range values {
		set[v] = true
	}
	return set
}
