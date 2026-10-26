package server

import (
	"net/http"
	"slices"

	apimeta "k8s.io/apimachinery/pkg/api/meta"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
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
}

// ownershipAccess is what the request's user may read; without
// authentication, everything.
func (s *Server) ownershipAccess(r *http.Request, catalog *ownership.Catalog) ownership.Access {
	if auth.UserFromContext(r.Context()) == nil {
		return ownership.Unrestricted()
	}
	namespaces, failed := s.getUserNamespacesWithStatus(r, nil)
	return &requestAccess{s: s, r: r, catalog: catalog, namespaces: namespaces, failed: failed}
}

// CanRead's checks are memoized on the user's permissions, so asking again
// costs a lookup.
func (a *requestAccess) CanRead(kind resourceid.GroupKind, namespace string) ownership.Decision {
	resource, namespaced, known := a.resource(kind)
	switch {
	case !known && a.catalog.Ambiguous(kind):
		// Two resources serve the kind, so no one grant decides it. Denied
		// would drop a gap on it, and an answer would claim to be complete.
		return ownership.Undecided
	case !known:
		// No served resource names it, so there is nothing to read: the read
		// says why. Denied would count its objects as hidden from the user.
		return ownership.Allowed
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
// reaches. A kind checked per namespace is allowed by a cluster-wide grant,
// and otherwise undecided rather than checked namespace by namespace.
func (a *requestAccess) anyNamespace(kind resourceid.GroupKind, resource string) ownership.Decision {
	switch {
	case a.failed:
		return ownership.Undecided
	case !checkedInNamespace(kind):
		if a.namespaces == nil || len(a.namespaces) > 0 {
			return ownership.Allowed
		}
		return ownership.Denied
	case a.check(kind.Group, resource, "") == ownership.Allowed:
		return ownership.Allowed
	}
	return ownership.Undecided
}

// checkedInNamespace reports the namespaced kinds authorized per namespace
// beyond namespace access: Secrets and Calico's policies, as the relationship
// graph strips them, and LimitRanges and Trivy's reports, as a resource GET
// checks them.
func checkedInNamespace(kind resourceid.GroupKind) bool {
	switch kind.Group {
	case "":
		return kind.Kind == "Secret" || kind.Kind == "LimitRange"
	case "projectcalico.org", "crd.projectcalico.org", aicontext.TrivyGroup:
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
	user := auth.UserFromContext(r.Context()) != nil
	if user {
		cachedTopo = s.relationshipTopologyForUser(r, cachedTopo)
		relIdx = nil
	}
	rel := topology.GetRelationshipsWithObject(kind, namespace, name, resource, cachedTopo,
		k8s.NewTopologyResourceProvider(cache),
		k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()), relIdx)
	if user && rel != nil {
		s.withholdDirectLookups(r, rel)
	}
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

// withholdDirectLookups drops what the user may not list from the
// relationships read straight from the shared caches rather than the
// user's graph: a StorageClass's volumes and the Pods on a Node.
func (s *Server) withholdDirectLookups(r *http.Request, rel *topology.Relationships) {
	if len(rel.Pods) > 0 {
		if namespaces := s.getUserNamespaces(r, nil); namespaces != nil {
			rel.Pods = slices.DeleteFunc(rel.Pods, func(p topology.ResourceRef) bool { return !slices.Contains(namespaces, p.Namespace) })
		}
	}
	isVolume := func(c topology.ResourceRef) bool { return c.Kind == "PersistentVolume" && c.Group == "" }
	if slices.ContainsFunc(rel.Consumers, isVolume) && !s.canRead(r, "", "persistentvolumes", "", "list") {
		rel.Consumers = slices.DeleteFunc(rel.Consumers, isVolume)
	}
}
