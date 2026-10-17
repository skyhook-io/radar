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
// API server: for a search, every candidate must be in the domain, once,
// name the UID in its current copy and match that copy exactly; every
// disputed or listed object in the domain it leaves out must not name it now;
// and the gaps must be the expected ones. Objects created after the list and
// never disputed aren't checked.
func (st settle) recheck(d ownershiptest.Difference, listed map[resourceid.Ref]ownershiptest.Object, cache ownership.Session, api apiRead) outcome {
	if d.Coverage || len(d.Objects) == 0 {
		return unresolved
	}
	changed := false
	current := map[resourceid.Ref]ownershiptest.Object{}
	exists := map[resourceid.Ref]bool{}
	settleRef := func(ref resourceid.Ref) bool {
		o, present, ok := st.settled(ref, cache, api)
		if ok {
			current[ref], exists[ref] = o, present
			changed = changed || changedSince(listed, ref, o, present)
		}
		return ok
	}
	for _, ref := range d.Objects {
		if !settleRef(ref) {
			return unresolved
		}
	}
	if d.Candidates {
		fresh := cache.OwnerCandidates(d.UID, d.Domain, d.Limit)
		if fresh.Truncated || !slices.Equal(fresh.Gaps, d.WantGaps) {
			return unresolved
		}
		seen := map[resourceid.Ref]bool{}
		for _, c := range fresh.Candidates {
			if seen[c.Ref] || !inDomain(c.Ref, d.Domain) {
				return unresolved
			}
			seen[c.Ref] = true
			if _, seen := current[c.Ref]; !seen && !settleRef(c.Ref) {
				return unresolved
			}
			if !exists[c.Ref] || !names(current[c.Ref].Owners, d.UID) || !ownershiptest.SameObservation(c, ownershiptest.Observe(current[c.Ref])) {
				return unresolved
			}
		}
		for _, ref := range d.Objects {
			if !seen[ref] && inDomain(ref, d.Domain) && exists[ref] && names(current[ref].Owners, d.UID) {
				return unresolved
			}
		}
		// A listed object that named the UID and still does must be in the
		// fresh answer too, unless the answer says its kind went unread.
		for ref, o := range listed {
			if seen[ref] || !inDomain(ref, d.Domain) || !names(o.Owners, d.UID) || unread(fresh.Gaps, ref) {
				continue
			}
			if !settleRef(ref) || exists[ref] && names(current[ref].Owners, d.UID) {
				return unresolved
			}
		}
	}
	if changed {
		return confirmed
	}
	return caughtUp
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

// unread reports whether gaps say ref's kind went unread where ref is.
func unread(gaps []ownership.Gap, ref resourceid.Ref) bool {
	return slices.ContainsFunc(gaps, func(g ownership.Gap) bool {
		return g.Kind == (resourceid.GroupKind{}) || g.Kind == ref.GroupKind() && (g.Namespace == "" || g.Namespace == ref.Namespace)
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
