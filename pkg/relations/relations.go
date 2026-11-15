// Package relations answers what an object references and what references
// it, from what Radar's informer caches already hold, for one caller. It
// composes pkg/ownership: the same cache session, the same Access and the
// same coverage vocabulary, with families of references beside ownership's
// owner references and GitOps membership.
//
// Each answer says what it couldn't cover. An empty answer with complete
// coverage means none was observed in what Radar collects, never that none
// exists anywhere.
package relations

import (
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// Session reads Radar's caches for relations: an ownership.ReadSession plus
// index lookups and the cached objects themselves, which families need to
// read what an object declares. Like ownership's, a Session does no I/O, no
// authorization, and is not safe for concurrent use.
type Session interface {
	ownership.ReadSession
	// Indexed reads the objects of gk in scope that index files under any of
	// keys, from stores loaded in full, each object once, with a gap for each
	// part of scope it couldn't read. A kind discovery doesn't serve is read
	// as empty only when discovery was complete. The objects are the cached
	// ones: callers must not modify or keep them.
	Indexed(gk resourceid.GroupKind, index string, keys []string, scope ownership.SearchScope) IndexedRead
	// Borrow is Object, also lending the cached object when it is Present.
	// The caller must not modify or keep it.
	Borrow(ref resourceid.Ref) (runtime.Object, ownership.ObjectRead)
}

// IndexedRead is what an Indexed read found, sorted by Ref, and the parts of
// its scope it couldn't read.
type IndexedRead struct {
	Objects []IndexedObject
	Gaps    []ownership.Gap
}

// IndexedObject is one cached object an index returned.
type IndexedObject struct {
	Ref    resourceid.Ref
	Object runtime.Object
}

// Coverage says what an answer couldn't cover.
type Coverage struct {
	// NotChecked are the kinds and namespaces Radar couldn't read, and why.
	NotChecked []ownership.Gap
	// Truncated: the answer lists the first Limit of Total.
	Truncated bool
	// Total is how many the answer found before the limit, when Truncated.
	Total int
	// Narrowed: the caller may not read some of what the answer covers, so
	// it lists only what the caller may read.
	Narrowed bool
}

// Complete reports whether the answer lists everything it found in all it
// set out to read.
func (c Coverage) Complete() bool {
	return len(c.NotChecked) == 0 && !c.Truncated
}

// View answers for one caller from one session. Like ownership.View it is
// not safe for concurrent use, and its answers hold only if the session's
// ValidateCaches passes once they are built.
type View struct {
	session Session
	access  ownership.Access
	limit   int
	decided map[accessKey]ownership.Decision
}

type accessKey struct {
	gk        resourceid.GroupKind
	namespace string
}

// DefaultLimit is the most items an answer lists.
const DefaultLimit = 500

// NewView answers for the caller access describes, from s, listing at most
// DefaultLimit items per answer.
func NewView(s Session, access ownership.Access) *View {
	return &View{session: s, access: access, limit: DefaultLimit, decided: map[accessKey]ownership.Decision{}}
}

// WithLimit sets the most items an answer lists.
func (v *View) WithLimit(limit int) *View {
	v.limit = limit
	return v
}

// mayRead reports whether the caller may read gk in namespace. Only Allowed
// permits: an undecided check reads as denied.
func (v *View) mayRead(gk resourceid.GroupKind, namespace string) bool {
	key := accessKey{gk, namespace}
	d, seen := v.decided[key]
	if !seen {
		d = v.access.CanRead(gk, namespace)
		v.decided[key] = d
	}
	return d == ownership.Allowed
}
