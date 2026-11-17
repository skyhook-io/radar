package server

import (
	"net/http"
	"slices"

	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

// requestAccess is what one request's user may read through Related
// Resources and the relation families: namespace
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
	exactGet   bool
}

// ownershipAccess is what the request's user may read; without
// authentication, everything.
func (s *Server) ownershipAccess(r *http.Request, catalog *ownership.Catalog) ownership.Access {
	return s.newOwnershipAccess(r, catalog, false)
}

func (s *Server) agentOwnershipAccess(r *http.Request, catalog *ownership.Catalog) ownership.Access {
	return s.newOwnershipAccess(r, catalog, true)
}

func (s *Server) newOwnershipAccess(r *http.Request, catalog *ownership.Catalog, exactGet bool) ownership.Access {
	if auth.UserFromContext(r.Context()) == nil {
		return ownership.Unrestricted()
	}
	namespaces, failed := s.getUserNamespacesWithStatus(r, nil)
	return &requestAccess{s: s, r: r, catalog: catalog, namespaces: namespaces, failed: failed, exactGet: exactGet}
}

// CanRead's checks are memoized on the user's permissions, so asking again
// costs a lookup.
func (a *requestAccess) CanRead(kind resourceid.GroupKind, namespace string) ownership.Decision {
	if !a.exactGet {
		return a.canList(kind, namespace)
	}
	resource, namespaced, known := a.resource(kind)
	switch {
	case !known && a.catalog.Ambiguous(kind):
		return ownership.Undecided
	case !known:
		return ownership.Allowed
	case namespace != "" && a.failed:
		return ownership.Undecided
	case namespace != "" && a.namespaces != nil && !slices.Contains(a.namespaces, namespace):
		return ownership.Denied
	}
	allowed, decided := a.s.canReadDecision(a.r, kind.Group, resource, namespace, "get")
	switch {
	case !decided:
		return ownership.Undecided
	case allowed:
		return ownership.Allowed
	case namespaced && namespace == "":
		return ownership.Undecided
	default:
		return ownership.Denied
	}
}

func (a *requestAccess) canList(kind resourceid.GroupKind, namespace string) ownership.Decision {
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
// beyond namespace access: Secrets, Calico policies, LimitRanges and Trivy reports.
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

// relationshipsFor answers a fetched resource's Related Resources within the
// request's namespace and kind access. Missing or replaced caches leave the
// answer unavailable so the write guard cannot mistake it for no manager.
func (s *Server) relationshipsFor(r *http.Request, resource any) *topology.Relationships {
	obj, ok := resource.(runtime.Object)
	if !ok {
		return nil
	}
	access := func(c *ownership.Catalog) ownership.Access { return s.ownershipAccess(r, c) }
	rel, _, _ := k8s.AnswerRelationships(k8s.GetResourceCache(), obj, access, access, "")
	return rel
}
