// Package ownershiplive checks Radar's ownership source against a live
// cluster: build with the liveparity tag and point it at one (see
// TestLiveParity). It only lists, watches and gets.
package ownershiplive

import (
	"slices"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// apiRead reads an object's current state straight from the API server.
type apiRead func(ref resourceid.Ref) (o ownershiptest.Object, exists bool, err error)

// settle bounds how long a check waits for the cache to catch up with the API
// server, which it follows through a watch a moment behind.
type settle struct {
	tries int
	pause time.Duration
}

var liveSettle = settle{tries: 20, pause: 100 * time.Millisecond}

// outcome is what rechecking one difference against the API server showed.
type outcome uint8

const (
	// unresolved: the cache still answers wrong after catching up, or the
	// difference is about coverage. The live check fails on it.
	unresolved outcome = iota
	// confirmed: the cache now answers right, and the API server shows the
	// disputed objects changed since the list.
	confirmed
	// caughtUp: the cache now answers right, but the API server shows no
	// change since the list. The cache was behind, or answered wrong for a
	// while; the check can't tell which.
	caughtUp
)

// settled reads ref from the cache and from the API server until both say
// the same: absent, or present in the same state. It reports what the API
// server says, and false if they never agreed.
func (st settle) settled(ref resourceid.Ref, cache ownership.Session, api apiRead) (ownershiptest.Object, bool, bool) {
	for try := range st.tries {
		if try > 0 {
			time.Sleep(st.pause)
		}
		read := cache.Object(ref)
		o, exists, err := api(ref)
		if err != nil {
			return o, false, false
		}
		switch {
		case !exists && read.State == ownership.Absent:
			return o, false, true
		case exists && read.State == ownership.Present && ownershiptest.SameObservation(read.Object, ownershiptest.Observe(o)):
			return o, true, true
		}
	}
	return ownershiptest.Object{}, false, false
}

// changedSince reports whether the API server's current state of an object
// differs from its listed copy.
func changedSince(listed map[resourceid.Ref]ownershiptest.Object, ref resourceid.Ref, now ownershiptest.Object, exists bool) bool {
	was, wasListed := listed[ref]
	if wasListed != exists {
		return true
	}
	return exists && (was.UID != now.UID || was.ResourceVersion != now.ResourceVersion)
}

// recheck asks a difference's question of the cache again once the objects
// it disputes have caught up, and checks the whole fresh answer against the
// API server's current copies (see the holds methods). Coverage differences
// are unresolved.
func (st settle) recheck(d ownershiptest.Difference, listed map[resourceid.Ref]ownershiptest.Object, cache ownership.Session, api apiRead) outcome {
	if d.Coverage || len(d.Objects) == 0 {
		return unresolved
	}
	r := &rechecking{settle: st, listed: listed, cache: cache, api: api, current: map[resourceid.Ref]ownershiptest.Object{}, exists: map[resourceid.Ref]bool{}}
	for _, ref := range d.Objects {
		if !r.settleRef(ref) {
			return unresolved
		}
	}
	holds := true
	switch d.Asked {
	case ownershiptest.AskedCandidates:
		holds = r.candidatesHold(d)
	case ownershiptest.AskedRecorders:
		holds = r.recordersHold(d)
	case ownershiptest.AskedInventory:
		holds = r.inventoryHolds(d)
	}
	switch {
	case !holds:
		return unresolved
	case r.changed:
		return confirmed
	}
	return caughtUp
}

// rechecking is one recheck's settled objects: each object's current copy,
// and whether any changed since the list.
type rechecking struct {
	settle
	listed  map[resourceid.Ref]ownershiptest.Object
	cache   ownership.Session
	api     apiRead
	current map[resourceid.Ref]ownershiptest.Object
	exists  map[resourceid.Ref]bool
	changed bool
}

// settleRef settles ref, once.
func (r *rechecking) settleRef(ref resourceid.Ref) bool {
	if _, done := r.exists[ref]; done {
		return true
	}
	o, present, ok := r.settled(ref, r.cache, r.api)
	if ok {
		r.current[ref], r.exists[ref] = o, present
		r.changed = r.changed || changedSince(r.listed, ref, o, present)
	}
	return ok
}

// candidatesHold: the fresh search is complete, with the expected gaps; every
// candidate is in the domain, once, names the UID in its current copy and
// matches that copy exactly; and every disputed or listed object in the
// domain it leaves out doesn't name the UID now, unless the answer says its
// kind went unread. Objects created after the list and never disputed aren't
// checked.
func (r *rechecking) candidatesHold(d ownershiptest.Difference) bool {
	fresh := r.cache.OwnerCandidates(d.UID, d.Domain, d.Limit)
	if fresh.Truncated || !slices.Equal(fresh.Gaps, d.WantGaps) {
		return false
	}
	seen := map[resourceid.Ref]bool{}
	for _, c := range fresh.Candidates {
		if seen[c.Ref] || !inDomain(c.Ref, d.Domain) || !r.settleRef(c.Ref) {
			return false
		}
		seen[c.Ref] = true
		if !r.exists[c.Ref] || !names(r.current[c.Ref].Owners, d.UID) || !ownershiptest.SameObservation(c, ownershiptest.Observe(r.current[c.Ref])) {
			return false
		}
	}
	for _, ref := range d.Objects {
		if !seen[ref] && inDomain(ref, d.Domain) && r.exists[ref] && names(r.current[ref].Owners, d.UID) {
			return false
		}
	}
	for ref, o := range r.listed {
		if seen[ref] || !inDomain(ref, d.Domain) || !names(o.Owners, d.UID) || unread(fresh.Gaps, ref) {
			continue
		}
		if !r.settleRef(ref) || r.exists[ref] && names(r.current[ref].Owners, d.UID) {
			return false
		}
	}
	return true
}

// recordersHold: the fresh search has the expected gaps; every recorder
// appears once, lists the object in its current copy and matches that copy;
// every unreadable manager's current list is unreadable; and every disputed
// manager left out neither lists the object nor has an unreadable list.
func (r *rechecking) recordersHold(d ownershiptest.Difference) bool {
	fresh := r.cache.Recorders(d.Ref)
	if !slices.Equal(fresh.Gaps, d.WantGaps) {
		return false
	}
	seen := map[resourceid.Ref]bool{}
	for _, c := range fresh.Recorders {
		if seen[c.Ref] || !r.settleRef(c.Ref) {
			return false
		}
		seen[c.Ref] = true
		o := r.current[c.Ref]
		if !r.exists[c.Ref] || o.Membership != ownership.MembersRecorded || !slices.Contains(o.Members, d.Ref) || !ownershiptest.SameObservation(c, ownershiptest.Observe(o)) {
			return false
		}
	}
	for _, m := range fresh.Unreadable {
		if seen[m] || !r.settleRef(m) {
			return false
		}
		seen[m] = true
		if !r.exists[m] || r.current[m].Membership != ownership.MembersUnreadable {
			return false
		}
	}
	for _, ref := range d.Objects {
		o := r.current[ref]
		if !seen[ref] && r.exists[ref] && (o.Membership == ownership.MembersUnreadable || o.Membership == ownership.MembersRecorded && slices.Contains(o.Members, d.Ref)) {
			return false
		}
	}
	return true
}

// inventoryHolds: the fresh read of the manager matches its current copy, and
// so does what it says of its members.
func (r *rechecking) inventoryHolds(d ownershiptest.Difference) bool {
	fresh := r.cache.Inventory(d.Ref)
	if !r.exists[d.Ref] {
		return fresh.Manager.State == ownership.Absent
	}
	o := r.current[d.Ref]
	if fresh.Manager.State != ownership.Present || !ownershiptest.SameObservation(fresh.Manager.Object, ownershiptest.Observe(o)) || fresh.Membership != o.Membership {
		return false
	}
	return o.Membership != ownership.MembersRecorded || slices.Equal(fresh.Members, o.Members)
}

// recheckResolution is recheck for a ResolveOwner difference: once the owner
// has caught up, the cache's resolution must be what the owner's current
// state implies. The dependent need not have changed.
func (st settle) recheckResolution(want, got ownership.OwnerResolution, dependent ownership.Observation, ref metav1.OwnerReference, listed map[resourceid.Ref]ownershiptest.Object, cache ownership.Session, api apiRead) outcome {
	if want.Ref != got.Ref || want.Ref == (resourceid.Ref{}) || want.Gap != got.Gap {
		return unresolved
	}
	o, exists, ok := st.settled(want.Ref, cache, api)
	if !ok {
		return unresolved
	}
	implied := ownership.OwnerGone
	if exists {
		implied = ownership.OwnerReplaced
		if o.UID == ref.UID {
			implied = ownership.OwnerFound
		}
	}
	if ownership.ResolveOwner(cache, dependent, ref).State != implied {
		return unresolved
	}
	if changedSince(listed, want.Ref, o, exists) {
		return confirmed
	}
	return caughtUp
}

// unread reports whether gaps say ref's kind went unread where ref is. A
// gap without a kind is about the search itself (an empty namespace entry, or
// kinds discovery didn't list), not about a kind Radar reads.
func unread(gaps []ownership.Gap, ref resourceid.Ref) bool {
	return slices.ContainsFunc(gaps, func(g ownership.Gap) bool {
		return g.Kind == ref.GroupKind() && (g.Namespace == "" || g.Namespace == ref.Namespace)
	})
}

// inDomain reports whether a search of d covers ref, an object of a
// cluster-scoped kind when it has no namespace.
func inDomain(ref resourceid.Ref, d ownership.Domain) bool {
	if ref.Namespace == "" {
		return d.ClusterScoped
	}
	return d.AllNamespaces || slices.Contains(d.Namespaces, ref.Namespace)
}

func names(owners []metav1.OwnerReference, uid types.UID) bool {
	return slices.ContainsFunc(owners, func(r metav1.OwnerReference) bool { return r.UID == uid })
}
