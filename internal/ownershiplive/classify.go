// Package ownershiplive checks Radar's ownership source against a live
// cluster: build with the liveparity tag and point it at one (see
// TestLiveParity). It only lists and watches.
package ownershiplive

import (
	"slices"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// apiRead reads an object's current state straight from the API server.
type apiRead func(ref resourceid.Ref) (o ownershiptest.Object, exists bool, err error)

// settleTries bounds how long a check waits for the cache to catch up with
// the API server, which it follows through a watch a moment behind.
var settleTries, settlePause = 20, 100 * time.Millisecond

// settled reads ref from the cache and from the API server until both say
// the same: absent, or present in the same state. It reports what the API
// server says, and false if they never agreed.
func settled(ref resourceid.Ref, cache ownership.Session, api apiRead) (ownershiptest.Object, bool, bool) {
	for try := range settleTries {
		if try > 0 {
			time.Sleep(settlePause)
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

// explained reports whether a difference between a listed World and a cache
// session is explained by the cluster changing while they were compared. It
// is, only when for every object the difference disputes the cache now
// agrees with a fresh read from the API server, and the cache's answer to the
// disputed question now matches what those reads imply. A difference about
// coverage never is.
func explained(d ownershiptest.Difference, cache ownership.Session, api apiRead) bool {
	if d.Coverage || len(d.Objects) == 0 {
		return false
	}
	var now ownership.CandidateRead
	if d.Candidates {
		// A cut-off answer can't show whether it holds an object.
		if now = cache.OwnerCandidates(d.UID, d.Domain, d.Limit); now.Truncated {
			return false
		}
	}
	for _, ref := range d.Objects {
		o, exists, ok := settled(ref, cache, api)
		if !ok {
			return false
		}
		if !d.Candidates {
			continue
		}
		named := exists && slices.ContainsFunc(o.Owners, func(r metav1.OwnerReference) bool { return r.UID == d.UID })
		if named != slices.ContainsFunc(now.Candidates, func(c ownership.Observation) bool { return c.Ref == ref }) {
			return false
		}
	}
	return true
}

// explainedResolution reports whether a ResolveOwner difference is explained
// by the owner changing while the two were compared: the cache now agrees
// with the API server about the owner, and its resolution now is what the
// owner's current state implies.
func explainedResolution(want, got ownership.OwnerResolution, dependent ownership.Observation, ref metav1.OwnerReference, cache ownership.Session, api apiRead) bool {
	if want.Ref != got.Ref || want.Ref == (resourceid.Ref{}) || want.Gap != got.Gap {
		return false
	}
	o, exists, ok := settled(want.Ref, cache, api)
	if !ok {
		return false
	}
	implied := ownership.OwnerGone
	if exists {
		implied = ownership.OwnerReplaced
		if o.UID == ref.UID {
			implied = ownership.OwnerFound
		}
	}
	return ownership.ResolveOwner(cache, dependent, ref).State == implied
}
