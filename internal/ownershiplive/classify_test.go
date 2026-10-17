package ownershiplive

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// candidateDrop answers like its session but drops every candidate: a source
// with a broken owner index.
type candidateDrop struct{ ownership.Session }

func (c candidateDrop) OwnerCandidates(uid types.UID, d ownership.Domain, limit int) ownership.CandidateRead {
	r := c.Session.OwnerCandidates(uid, d, limit)
	r.Candidates = nil
	return r
}

// falseAbsence answers like its session but reports one object absent.
type falseAbsence struct {
	ownership.Session
	ref resourceid.Ref
}

func (f falseAbsence) Object(ref resourceid.Ref) ownership.ObjectRead {
	if ref == f.ref {
		return ownership.ObjectRead{State: ownership.Absent}
	}
	return f.Session.Object(ref)
}

func world(objects ...ownershiptest.Object) ownershiptest.World {
	w := ownershiptest.World{Resources: ownershiptest.Resources(), Objects: objects, Collection: map[resourceid.GroupKind]ownershiptest.Collection{}}
	for _, k := range ownershiptest.Kinds {
		w.Collection[k.GroupKind] = ownershiptest.Collection{OnDemand: !k.Typed}
	}
	return w
}

// apiOf serves a World's objects as the API server's current state.
func apiOf(w ownershiptest.World) apiRead {
	return func(ref resourceid.Ref) (ownershiptest.Object, bool, error) {
		for _, o := range w.Objects {
			if o.Ref == ref {
				return o, true, nil
			}
		}
		return ownershiptest.Object{}, false, nil
	}
}

func begin(t *testing.T, w ownershiptest.World) ownership.Session {
	t.Helper()
	s, err := ownershiptest.Scan(w).Begin()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestExplainedNeedsTheCacheToAgreeWithTheAPIServer(t *testing.T) {
	settleTries, settlePause = 2, time.Millisecond
	rs := ownershiptest.Object{Ref: resourceid.Ref{Group: "apps", Kind: "ReplicaSet", Namespace: "alpha", Name: "rs"}, APIVersion: "apps/v1", UID: "uid-rs", ResourceVersion: "1"}
	pod := ownershiptest.Object{Ref: resourceid.Ref{Kind: "Pod", Namespace: "alpha", Name: "p"}, APIVersion: "v1", UID: "uid-p", ResourceVersion: "10", Owners: []metav1.OwnerReference{rs.OwnerRef(true)}}
	updated := pod
	updated.ResourceVersion = "11"
	orphaned := updated
	orphaned.Owners = nil

	listed := world(rs, pod)
	q := ownershiptest.Queries{Refs: []resourceid.Ref{pod.Ref}, UIDs: []types.UID{"uid-rs"}, Domains: []ownership.Domain{ownership.InNamespace("alpha")}, Limits: []int{0}}
	for _, tc := range []struct {
		name  string
		now   ownershiptest.World
		cache func(ownership.Session) ownership.Session
		// want says, per question, whether its difference is explained.
		want map[string]bool
	}{
		{"the Pod was updated, and the cache has the update", world(rs, updated), nil, map[string]bool{"Object": true, "OwnerCandidates": true}},
		{"the Pod lost its owner, and the cache knows", world(rs, orphaned), nil, map[string]bool{"Object": true, "OwnerCandidates": true}},
		{"the Pod was deleted, and the cache knows", world(rs), nil, map[string]bool{"Object": true, "OwnerCandidates": true}},
		// The cache's own absence is not evidence: the API server still has it.
		{"the cache falsely reports the Pod absent", world(rs, pod), func(s ownership.Session) ownership.Session { return falseAbsence{s, pod.Ref} }, map[string]bool{"Object": false}},
		// The Pod's update explains its Object difference, but a write that
		// kept its owner doesn't explain a missing candidate.
		{"a status write, and the index drops the Pod", world(rs, updated), func(s ownership.Session) ownership.Session { return candidateDrop{s} }, map[string]bool{"Object": true, "OwnerCandidates": false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := begin(t, tc.now)
			if tc.cache != nil {
				cache = tc.cache(cache)
			}
			diffs := ownershiptest.Differences(begin(t, listed), cache, q)
			seen := map[string]bool{}
			for _, d := range diffs {
				question := "Object"
				if d.Candidates {
					question = "OwnerCandidates"
				}
				seen[question] = true
				want, expected := tc.want[question]
				if !expected {
					t.Fatalf("unexpected difference %s", d.Question)
				}
				if got := explained(d, cache, apiOf(tc.now)); got != want {
					t.Errorf("%s: explained = %v, want %v", d.Question, got, want)
				}
			}
			for question := range tc.want {
				if !seen[question] {
					t.Errorf("scenario: no %s difference", question)
				}
			}
		})
	}
}

func TestExplainedResolutionNeedsOnlyTheOwnerToChange(t *testing.T) {
	settleTries, settlePause = 2, time.Millisecond
	rs := ownershiptest.Object{Ref: resourceid.Ref{Group: "apps", Kind: "ReplicaSet", Namespace: "alpha", Name: "rs"}, APIVersion: "apps/v1", UID: "uid-rs", ResourceVersion: "1"}
	pod := ownershiptest.Object{Ref: resourceid.Ref{Kind: "Pod", Namespace: "alpha", Name: "p"}, APIVersion: "v1", UID: "uid-p", ResourceVersion: "10", Owners: []metav1.OwnerReference{rs.OwnerRef(true)}}
	recreated := rs
	recreated.UID, recreated.ResourceVersion = "uid-rs-again", "2"
	listed := begin(t, world(rs, pod))
	dependent := listed.Object(pod.Ref).Object
	ref := dependent.OwnerReferences[0]
	for _, tc := range []struct {
		name string
		now  ownershiptest.World
		want bool
	}{
		{"the owner was recreated", world(recreated, pod), true},
		{"the owner was deleted", world(pod), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := begin(t, tc.now)
			a, b := ownership.ResolveOwner(listed, dependent, ref), ownership.ResolveOwner(cache, dependent, ref)
			if a.State == b.State {
				t.Fatal("scenario: the resolutions should differ")
			}
			if got := explainedResolution(a, b, dependent, ref, cache, apiOf(tc.now)); got != tc.want {
				t.Fatalf("explained = %v, want %v (%v vs %v)", got, tc.want, a.State, b.State)
			}
		})
	}
	// The owner unchanged, yet the cache resolves it differently: a defect.
	unchanged := world(rs, pod)
	lying := falseAbsence{begin(t, unchanged), rs.Ref}
	a, b := ownership.ResolveOwner(listed, dependent, ref), ownership.ResolveOwner(lying, dependent, ref)
	if explainedResolution(a, b, dependent, ref, lying, apiOf(unchanged)) {
		t.Fatal("a wrong resolution of an unchanged owner was explained")
	}
}
