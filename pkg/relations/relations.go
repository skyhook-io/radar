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
	"slices"

	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
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
	// part of scope it couldn't read. An empty index reads every object of
	// gk in scope; an index with no keys reads no object but reports the
	// same gaps. A kind discovery doesn't serve is read as empty only when
	// discovery covered its group, so one failing API group leaves other
	// groups' absent kinds empty. The objects are the cached ones: callers
	// must not modify or keep them.
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
	// SubjectState is "replaced" when the cache holds another incarnation.
	SubjectState string

	// NotChecked are the kinds and namespaces Radar couldn't read, or where
	// it couldn't check the caller's access, and why.
	NotChecked []ownership.Gap
	// Truncated: the answer lists the first Limit of Total.
	Truncated bool
	// Total is how many the answer found before the limit, when Truncated.
	Total int
	// Narrowed: the caller may not read some of what the answer covers, so
	// it lists only what the caller may read. A References answer still
	// lists a withheld declaration (TargetWithheld), named as the subject's
	// own spec names it; a host must not present it as readable.
	Narrowed bool
}

// Complete reports whether the answer lists everything it found in all it
// set out to read. Narrowed doesn't affect it: a narrowed answer can be
// complete for what the caller may read.
func (c Coverage) Complete() bool {
	return c.SubjectState == "" && len(c.NotChecked) == 0 && !c.Truncated
}

// View answers for one caller from one session. Like ownership.View it is
// not safe for concurrent use, and its answers hold only if the session's
// ValidateCaches passes once they are built.
type View struct {
	session       Session
	lookupSession Session
	access        ownership.Access
	limit         int
	decided       map[accessKey]ownership.Decision
}

type accessKey struct {
	gk        resourceid.GroupKind
	namespace string
}

// DefaultLimit is the most objects a Referrers, Selected, SelectedBy
// or Composed answer lists. References lists every declaration.
const DefaultLimit = 500

// NewView answers for the caller access describes, from s, listing at most
// DefaultLimit objects per Referrers, Selected, SelectedBy or Composed
// answer.
func NewView(s Session, access ownership.Access) *View {
	return &View{session: s, lookupSession: s, access: access, limit: DefaultLimit, decided: map[accessKey]ownership.Decision{}}
}

// WithAuxiliarySession supplies non-emitted selector inputs such as Namespace
// labels. Emission reads still use the original session and Access.
func (v *View) WithAuxiliarySession(s Session) *View {
	v.lookupSession = s
	return v
}

// WithLimit sets, on v itself, the most objects a Referrers, Selected,
// SelectedBy or Composed answer lists; a limit of zero or less lists every one. It
// returns v.
func (v *View) WithLimit(limit int) *View {
	v.limit = limit
	return v
}

// decide is the caller's access to gk in namespace, asked once per view.
func (v *View) decide(gk resourceid.GroupKind, namespace string) ownership.Decision {
	key := accessKey{gk, namespace}
	d, seen := v.decided[key]
	if !seen {
		d = v.access.CanRead(gk, namespace)
		v.decided[key] = d
	}
	return d
}

// mayRead reports whether the caller may read gk in namespace, recording in
// c why not: only Allowed permits; Denied narrows the answer to what the
// caller may read, and a check that couldn't be made leaves the answer
// incomplete, since it may have hidden anything.
func (v *View) mayRead(gk resourceid.GroupKind, namespace string, c *Coverage) bool {
	switch v.decide(gk, namespace) {
	case ownership.Allowed:
		return true
	case ownership.Undecided:
		c.addGap(ownership.Gap{Kind: gk, Namespace: namespace, Reason: readreason.UserPermissionCheckFailed})
	default:
		c.Narrowed = true
	}
	return false
}

// merge adds what other records to c.
func (c *Coverage) merge(other Coverage) {
	for _, g := range other.NotChecked {
		c.addGap(g)
	}
	c.Narrowed = c.Narrowed || other.Narrowed
}

func (c *Coverage) addGap(g ownership.Gap) {
	if !slices.Contains(c.NotChecked, g) {
		c.NotChecked = append(c.NotChecked, g)
	}
}
