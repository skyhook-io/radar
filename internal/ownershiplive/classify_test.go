package ownershiplive

import (
	"testing"

	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// cacheView answers Object from a fixed table, standing in for a cache.
type cacheView struct {
	ownership.Session
	reads map[resourceid.Ref]ownership.ObjectRead
}

func (c cacheView) Object(ref resourceid.Ref) ownership.ObjectRead {
	if r, ok := c.reads[ref]; ok {
		return r
	}
	return ownership.ObjectRead{State: ownership.Absent}
}

func TestWrittenBetweenNeedsProofOfAWrite(t *testing.T) {
	pod := resourceid.Ref{Kind: "Pod", Namespace: "alpha", Name: "p"}
	listed := map[resourceid.Ref]ownershiptest.Object{pod: {Ref: pod, UID: "uid-p", ResourceVersion: "10"}}
	present := func(uid, rv string) ownership.ObjectRead {
		return ownership.ObjectRead{State: ownership.Present, Object: ownership.Observation{Ref: pod, UID: types.UID("uid-" + uid), ResourceVersion: rv}}
	}
	about := ownershiptest.Difference{Question: "q", Objects: []resourceid.Ref{pod}}
	for _, tc := range []struct {
		name  string
		diff  ownershiptest.Difference
		cache ownership.ObjectRead
		list  map[resourceid.Ref]ownershiptest.Object
		want  bool
	}{
		{"updated since the list", about, present("p", "11"), listed, true},
		{"recreated since the list", about, present("q", "12"), listed, true},
		{"deleted since the list", about, ownership.ObjectRead{State: ownership.Absent}, listed, true},
		{"created since the list", about, present("p", "11"), map[resourceid.Ref]ownershiptest.Object{}, true},
		{"unchanged object", about, present("p", "10"), listed, false},
		{"unreadable in the cache", about, ownership.ObjectRead{}, listed, false},
		{"excluded in the cache", about, ownership.ObjectRead{State: ownership.Excluded}, listed, false},
		{"about coverage", ownershiptest.Difference{Question: "q", Objects: []resourceid.Ref{pod}, Coverage: true}, present("p", "11"), listed, false},
		{"about no object", ownershiptest.Difference{Question: "q"}, present("p", "11"), listed, false},
	} {
		cache := cacheView{reads: map[resourceid.Ref]ownership.ObjectRead{pod: tc.cache}}
		if got := writtenBetween(tc.diff, tc.list, cache); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}
