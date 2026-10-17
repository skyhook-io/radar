// Package ownershiplive checks Radar's ownership source against a live
// cluster: build with the liveparity tag and point it at one (see
// TestLiveParity). It only lists and watches.
package ownershiplive

import (
	"slices"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// writtenBetween reports whether a difference between a World listed from a
// cluster and a cache session over the same cluster is accounted for by the
// objects it involves being written between the two reads. Kubernetes gives
// an object a new resource version on every write, so a UID or resource
// version the two disagree on proves a write; anything else does not. A
// difference about coverage, or about an object the cache couldn't read, is
// never accounted for that way.
func writtenBetween(d ownershiptest.Difference, listed map[resourceid.Ref]ownershiptest.Object, cache ownership.Session) bool {
	if d.Coverage || len(d.Objects) == 0 {
		return false
	}
	return !slices.ContainsFunc(d.Objects, func(ref resourceid.Ref) bool {
		o, wasListed := listed[ref]
		read := cache.Object(ref)
		switch read.State {
		case ownership.Present:
			return wasListed && read.Object.UID == o.UID && read.Object.ResourceVersion == o.ResourceVersion
		case ownership.Absent:
			// Gone from a loaded store since the list, or never listed.
			return !wasListed
		default:
			return true
		}
	})
}
