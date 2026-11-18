package relations

import (
	"slices"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// heldSubject reports whether an answer about subject can be given: the
// caller may read it and Radar holds it. A subject Radar couldn't read adds
// its gap; one the host read (fetched) that the cache doesn't hold yet
// leaves the answer incomplete rather than empty. It returns the cached
// object.
func (v *View) heldSubject(subject resourceid.Ref, fetched runtime.Object, c *Coverage) (runtime.Object, bool) {
	if !v.mayRead(subject.GroupKind(), subject.Namespace, c) {
		return nil, false
	}
	obj, read := v.session.Borrow(subject)
	switch read.State {
	case ownership.Present:
		return obj, true
	case ownership.Unavailable:
		c.addGap(read.Gap)
	case ownership.Absent:
		if fetched != nil {
			c.addGap(ownership.Gap{Kind: subject.GroupKind(), Namespace: subject.Namespace, Reason: readreason.ObjectNotFound})
		}
	}
	return nil, false
}

// search is where an answer reads the objects that may relate to a subject:
// the subject's namespace, read through the namespace index every store
// keeps, or, across namespaces, as ownership.View.Children answers a
// cluster-scoped owner: the namespaces the caller may read something in,
// each object checked where it is, and a gap kept only if it covers what
// the caller may read.
type search struct {
	v         *View
	namespace string
	across    bool
	scope     ownership.SearchScope
	index     string
	keys      []string
}

func (v *View) newSearch(namespace string, across bool, c *Coverage) search {
	s := search{v: v, namespace: namespace, across: across, scope: ownership.InNamespace(namespace), index: cache.NamespaceIndex, keys: []string{namespace}}
	if across {
		namespaces, all := v.access.Namespaces()
		s.scope = ownership.SearchScope{AllNamespaces: all, Namespaces: namespaces, IncludeClusterScoped: true}
		s.index, s.keys = "", nil
		if !all {
			// Cluster-scoped objects file under "" in the namespace index.
			s.index, s.keys = cache.NamespaceIndex, append(slices.Clone(namespaces), "")
		}
		c.Narrowed = c.Narrowed || v.access != ownership.Unrestricted()
	}
	return s
}

// newSearchIn searches the namespaces a selector names, as an across search
// does: each object checked where it is, gaps kept only where the caller may
// read.
func (v *View) newSearchIn(namespaces []string, c *Coverage) search {
	namespaces = slices.Compact(slices.Sorted(slices.Values(namespaces)))
	c.Narrowed = c.Narrowed || v.access != ownership.Unrestricted()
	return search{v: v, across: true, scope: ownership.SearchScope{Namespaces: namespaces}, index: cache.NamespaceIndex, keys: namespaces}
}

// each hands fn every object of gk in the search the caller may read.
func (s search) each(gk resourceid.GroupKind, c *Coverage, fn func(IndexedObject)) {
	if !s.across && !s.v.mayRead(gk, s.namespace, c) {
		return
	}
	read := s.v.session.Indexed(gk, s.index, s.keys, s.scope)
	for _, g := range read.Gaps {
		if g.Namespace != "" && !s.scope.AllNamespaces && !slices.Contains(s.scope.Namespaces, g.Namespace) {
			continue
		}
		if !s.across || ownership.GapCoversReadable(s.v.access, s.v.session.Catalog(), g, s.scope) {
			c.addGap(g)
		}
	}
	for _, o := range read.Objects {
		// Saying a check failed would say an object may be there, so an
		// undecided one narrows the answer as a denied one does.
		if s.across && s.v.decide(gk, o.Ref.Namespace) != ownership.Allowed {
			c.Narrowed = true
			continue
		}
		fn(o)
	}
}
