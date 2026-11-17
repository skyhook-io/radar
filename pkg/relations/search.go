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
// keeps, or, across namespaces, every namespace Radar holds. Each object is
// matched against the subject first and checked against the caller's access
// only if it relates, so an answer says it withheld something exactly when a
// related object is hidden from the caller, and a kind with nothing related
// costs no permission check. A gap is kept only if it covers what the caller
// may read.
type search struct {
	v         *View
	namespace string
	across    bool
	scope     ownership.SearchScope
	// readable is what the caller may read of scope, for keeping gaps.
	readable ownership.SearchScope
	index    string
	keys     []string
}

func (v *View) newSearch(namespace string, across bool) search {
	s := search{v: v, namespace: namespace, across: across, scope: ownership.InNamespace(namespace), index: cache.NamespaceIndex, keys: []string{namespace}}
	s.readable = s.scope
	if across {
		s.scope = ownership.SearchScope{AllNamespaces: true, IncludeClusterScoped: true}
		s.index, s.keys = "", nil
		namespaces, all := v.access.Namespaces()
		s.readable = ownership.SearchScope{AllNamespaces: all, Namespaces: namespaces, IncludeClusterScoped: true}
	}
	return s
}

// newSearchIn searches the namespaces a selector names, as an across search
// does.
func (v *View) newSearchIn(namespaces []string) search {
	namespaces = slices.Compact(slices.Sorted(slices.Values(namespaces)))
	scope := ownership.SearchScope{Namespaces: namespaces}
	return search{v: v, across: true, scope: scope, readable: scope, index: cache.NamespaceIndex, keys: namespaces}
}

// needsKindAccess reports a gap the caller is told of only if they may read
// its kind; one Radar's setup causes names no object.
func needsKindAccess(g ownership.Gap) bool { return !g.FromRadarSetup() }

// each asks relate about every object of gk in the search and emits those
// that relate and the caller may read. relate records into its own coverage
// and returns how to emit the object, or nil when it doesn't relate; what it
// records about an object is kept only if the object is emitted, or, when
// relate couldn't tell (a gap, or a withheld candidate that stopped its
// resolution), if the caller may read it.
func (s search) each(gk resourceid.GroupKind, c *Coverage, relate func(o IndexedObject, c *Coverage) (emit func())) {
	read := s.v.session.Indexed(gk, s.index, s.keys, s.scope)
	if len(read.Gaps) > 0 && (s.across || !slices.ContainsFunc(read.Gaps, needsKindAccess) || s.v.mayRead(gk, s.namespace, c)) {
		for _, g := range read.Gaps {
			unreadable := g.Namespace != "" && !s.readable.AllNamespaces && !slices.Contains(s.readable.Namespaces, g.Namespace)
			switch {
			case !unreadable && (!s.across || ownership.GapCoversReadable(s.v.access, s.v.session.Catalog(), g, s.readable)):
				c.addGap(g)
			default:
				// Unread where the caller may not read, so a related object
				// there may be hidden.
				c.Narrowed = true
			}
		}
	}
	for _, o := range read.Objects {
		var own Coverage
		emit := relate(o, &own)
		if emit == nil && len(own.NotChecked) == 0 && !own.Narrowed {
			continue
		}
		if !s.admits(gk, o.Ref.Namespace, c) {
			continue
		}
		c.merge(own)
		if emit != nil {
			emit()
		}
	}
}

// admits decides whether the caller may read a related object of gk in
// namespace. A check that couldn't be made leaves the answer incomplete.
// Across namespaces its gap names no namespace, so it says no more than a
// denial's narrowing would: that a related object is somewhere the caller
// couldn't be checked.
func (s search) admits(gk resourceid.GroupKind, namespace string, c *Coverage) bool {
	if !s.across {
		return s.v.mayRead(gk, namespace, c)
	}
	switch s.v.decide(gk, namespace) {
	case ownership.Allowed:
		return true
	case ownership.Undecided:
		c.addGap(ownership.Gap{Kind: gk, Reason: readreason.UserPermissionCheckFailed})
	default:
		c.Narrowed = true
	}
	return false
}
