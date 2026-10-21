package server

import (
	"net/http"
	"slices"

	apimeta "k8s.io/apimachinery/pkg/api/meta"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

// requestAccess is what one request's user may read through Related
// Resources, by the rules the per-user relationship graph applies
// (relationshipTopologyForUser, applyClusterScopedTopologyRBAC): namespace
// access for namespaced kinds, with a list check per namespace for Secrets,
// LimitRanges and Calico's policies in either API group, and a list check for
// each cluster-scoped kind. Listing what an object owns enumerates names, so
// every check is a list check.
type requestAccess struct {
	s       *Server
	r       *http.Request
	catalog *ownership.Catalog
	// namespaces are the user's namespaces, nil for every one; failed: they
	// couldn't be discovered.
	namespaces []string
	failed     bool
	// clusterNamespaces lists every namespace, for a user who may read them
	// all.
	clusterNamespaces func() []string
	decided           map[accessKey]ownership.Decision
	everything        *bool
}

type accessKey struct {
	kind      resourceid.GroupKind
	namespace string
}

// ownershipAccess is what the request's user may read; without
// authentication, everything.
func (s *Server) ownershipAccess(r *http.Request, catalog *ownership.Catalog) ownership.Access {
	if auth.UserFromContext(r.Context()) == nil {
		return ownership.Unrestricted()
	}
	namespaces, failed := s.getUserNamespacesWithStatus(r, nil)
	return &requestAccess{s: s, r: r, catalog: catalog, namespaces: namespaces, failed: failed, clusterNamespaces: allNamespaceNames, decided: map[accessKey]ownership.Decision{}}
}

func (a *requestAccess) CanRead(kind resourceid.GroupKind, namespace string) ownership.Decision {
	key := accessKey{kind: kind, namespace: namespace}
	if d, ok := a.decided[key]; ok {
		return d
	}
	d := a.read(kind, namespace)
	a.decided[key] = d
	return d
}

func (a *requestAccess) read(kind resourceid.GroupKind, namespace string) ownership.Decision {
	resource, namespaced, known := a.resource(kind)
	switch {
	case !known && a.catalog.Ambiguous(kind):
		// Two resources serve the kind, so no one grant decides it. Denied
		// would drop a gap on it, and an answer would claim to be complete.
		return ownership.Undecided
	case !known:
		// Without its resource there is nothing to check, so nothing to allow.
		return ownership.Denied
	case namespace == "" && namespaced:
		return a.anyNamespace(kind, resource)
	case namespace == "":
		return a.check(kind.Group, resource, "")
	case a.failed:
		return ownership.Undecided
	case a.namespaces != nil && !slices.Contains(a.namespaces, namespace):
		return ownership.Denied
	case checkedInNamespace(kind):
		return a.check(kind.Group, resource, namespace)
	}
	return ownership.Allowed
}

// anyNamespace decides whether the user may read a namespaced kind in at
// least one of their namespaces: an ordinary kind wherever namespace access
// reaches, a kind checked per namespace by a cluster-wide grant or else a
// grant in one of them.
func (a *requestAccess) anyNamespace(kind resourceid.GroupKind, resource string) ownership.Decision {
	switch {
	case a.failed:
		return ownership.Undecided
	case !checkedInNamespace(kind):
		if a.namespaces == nil || len(a.namespaces) > 0 {
			return ownership.Allowed
		}
		return ownership.Denied
	}
	if a.check(kind.Group, resource, "") == ownership.Allowed {
		return ownership.Allowed
	}
	namespaces := a.namespaces
	if namespaces == nil {
		if namespaces = a.clusterNamespaces(); len(namespaces) == 0 {
			// Without the cluster's namespaces there is no telling.
			return ownership.Undecided
		}
	}
	return ownership.AnyNamespace(namespaces, func(namespace string) ownership.Decision {
		return a.check(kind.Group, resource, namespace)
	})
}

// checkedInNamespace reports the namespaced kinds authorized per namespace
// beyond namespace access: Secrets and Calico's policies, as the relationship
// graph strips them, and LimitRanges, as a resource GET checks them.
func checkedInNamespace(kind resourceid.GroupKind) bool {
	switch kind.Group {
	case "":
		return kind.Kind == "Secret" || kind.Kind == "LimitRange"
	case "projectcalico.org", "crd.projectcalico.org":
		return true
	}
	return false
}

func (a *requestAccess) check(group, resource, namespace string) ownership.Decision {
	allowed, decided := a.s.canReadDecision(a.r, group, resource, namespace, "list")
	switch {
	case !decided:
		return ownership.Undecided
	case allowed:
		return ownership.Allowed
	}
	return ownership.Denied
}

func (a *requestAccess) resource(kind resourceid.GroupKind) (resource string, namespaced, known bool) {
	if b, ok := resourceid.BuiltinForKind(kind.Kind); ok && b.Group == kind.Group {
		return b.Resource, b.Namespaced, true
	}
	if k, ok := a.catalog.Kind(kind); ok {
		return k.Resource, k.Namespaced, true
	}
	return "", false, false
}

func (a *requestAccess) Namespaces() ([]string, bool) {
	if a.failed {
		return []string{}, false
	}
	return a.namespaces, a.namespaces == nil
}

// AllKindsIn holds when the user may read namespace and every kind there
// that is checked per namespace.
func (a *requestAccess) CanReadAllKindsIn(namespace string) bool {
	if a.CanRead(resourceid.GroupKind{Kind: "Secret"}, namespace) != ownership.Allowed ||
		a.CanRead(resourceid.GroupKind{Kind: "LimitRange"}, namespace) != ownership.Allowed {
		return false
	}
	for _, k := range a.catalog.DependentKinds() {
		if k.Namespaced && checkedInNamespace(k.GroupKind) && a.CanRead(k.GroupKind, namespace) != ownership.Allowed {
			return false
		}
	}
	return true
}

// Everything holds for a user with every namespace and a wildcard read
// grant, as cluster-admin has.
func (a *requestAccess) CanReadEverything() bool {
	if a.everything == nil {
		all := !a.failed && a.namespaces == nil && a.check("*", "*", "") == ownership.Allowed
		a.everything = &all
	}
	return *a.everything
}

// relationshipsFor is a fetched resource's Related Resources: the links the
// relationship graph holds, with ownership answered from owner references
// for the request's user. It is nil until Radar has a graph, as while its
// caches sync or after a context switch: without one Radar can't say what
// manages the object, and the UI reads a missing field as owners not mapped
// yet, which its GitOps write guard warns about.
func (s *Server) relationshipsFor(r *http.Request, kind, namespace, name string, resource any) *topology.Relationships {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil
	}
	cachedTopo, relIdx := s.broadcaster.GetCachedTopologyWithIndex()
	if cachedTopo == nil {
		return nil
	}
	if auth.UserFromContext(r.Context()) != nil {
		cachedTopo = s.relationshipTopologyForUser(r, cachedTopo)
		relIdx = nil
	}
	rel := topology.GetRelationshipsWithObject(kind, namespace, name, resource, cachedTopo,
		k8s.NewTopologyResourceProvider(cache),
		k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()), relIdx)
	fetched, ok := k8s.OwnershipObservation(resource)
	if !ok {
		return rel
	}
	if rel == nil {
		rel = &topology.Relationships{}
	}
	owner, _ := apimeta.Accessor(resource)
	k8s.AnswerOwnership(rel, fetched, owner, func(c *ownership.Catalog) ownership.Access { return s.ownershipAccess(r, c) })
	return rel
}
