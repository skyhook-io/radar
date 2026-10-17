package ownershiplive

import (
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

var quick = settle{tries: 3, pause: time.Millisecond}

// candidateDrop drops every candidate: a broken owner index.
type candidateDrop struct{ ownership.Session }

func (c candidateDrop) OwnerCandidates(uid types.UID, d ownership.Domain, limit int) ownership.CandidateRead {
	r := c.Session.OwnerCandidates(uid, d, limit)
	r.Candidates = nil
	return r
}

// inventedUID returns every candidate under a UID it doesn't have.
type inventedUID struct{ ownership.Session }

func (c inventedUID) OwnerCandidates(uid types.UID, d ownership.Domain, limit int) ownership.CandidateRead {
	r := c.Session.OwnerCandidates(uid, d, limit)
	for i := range r.Candidates {
		r.Candidates[i].UID = "invented"
	}
	return r
}

// falseAbsence reports one object absent, always or only the first time.
type falseAbsence struct {
	ownership.Session
	ref   resourceid.Ref
	once  bool
	calls *int
}

func (f falseAbsence) Object(ref resourceid.Ref) ownership.ObjectRead {
	if ref == f.ref {
		*f.calls++
		if !f.once || *f.calls == 1 {
			return ownership.ObjectRead{State: ownership.Absent}
		}
	}
	return f.Session.Object(ref)
}

// extraCandidate adds an object to every search's candidates, as an index
// that ignores the domain would.
type extraCandidate struct {
	ownership.Session
	extra ownership.Observation
}

func (e extraCandidate) OwnerCandidates(uid types.UID, d ownership.Domain, limit int) ownership.CandidateRead {
	r := e.Session.OwnerCandidates(uid, d, limit)
	r.Candidates = append(r.Candidates, e.extra)
	return r
}

// twice returns every candidate twice.
type twice struct{ ownership.Session }

func (c twice) OwnerCandidates(uid types.UID, d ownership.Domain, limit int) ownership.CandidateRead {
	r := c.Session.OwnerCandidates(uid, d, limit)
	r.Candidates = append(r.Candidates, r.Candidates...)
	return r
}

// dropsLater answers the first search right, and leaves one object out of
// every later one.
type dropsLater struct {
	ownership.Session
	drop  resourceid.Ref
	calls *int
}

func (l dropsLater) OwnerCandidates(uid types.UID, d ownership.Domain, limit int) ownership.CandidateRead {
	r := l.Session.OwnerCandidates(uid, d, limit)
	if *l.calls++; *l.calls > 1 {
		r.Candidates = slices.DeleteFunc(r.Candidates, func(c ownership.Observation) bool { return c.Ref == l.drop })
	}
	return r
}

// earlyGap adds a gap to the first search only.
type earlyGap struct {
	ownership.Session
	calls *int
}

func (e earlyGap) OwnerCandidates(uid types.UID, d ownership.Domain, limit int) ownership.CandidateRead {
	r := e.Session.OwnerCandidates(uid, d, limit)
	if *e.calls++; *e.calls == 1 {
		r.Gaps = append(r.Gaps, ownership.Gap{Kind: resourceid.GroupKind{Kind: "Pod"}, Namespace: "beta", Reason: "kind_sync_pending"})
	}
	return r
}

// laterGap adds a gap to every search after the first.
type laterGap struct {
	ownership.Session
	calls *int
}

func (l laterGap) OwnerCandidates(uid types.UID, d ownership.Domain, limit int) ownership.CandidateRead {
	r := l.Session.OwnerCandidates(uid, d, limit)
	if *l.calls++; *l.calls > 1 {
		r.Gaps = append(r.Gaps, ownership.Gap{Kind: resourceid.GroupKind{Kind: "Pod"}, Namespace: "beta", Reason: "kind_sync_pending"})
	}
	return r
}

// lagging answers from an older view of the cluster until it has been asked
// twice, as a cache does while its watch catches up.
type lagging struct {
	ownership.Session
	old   ownership.Session
	calls *int
}

func (l lagging) view() ownership.Session {
	*l.calls++
	if *l.calls <= 2 {
		return l.old
	}
	return l.Session
}

func (l lagging) Object(ref resourceid.Ref) ownership.ObjectRead { return l.view().Object(ref) }

func (l lagging) OwnerCandidates(uid types.UID, d ownership.Domain, limit int) ownership.CandidateRead {
	return l.view().OwnerCandidates(uid, d, limit)
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

func listedOf(w ownershiptest.World) map[resourceid.Ref]ownershiptest.Object {
	out := map[resourceid.Ref]ownershiptest.Object{}
	for _, o := range w.Objects {
		out[o.Ref] = o
	}
	return out
}

func begin(t *testing.T, w ownershiptest.World) ownership.Session {
	t.Helper()
	s, err := ownershiptest.Scan(w).Begin()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRecheckJudgesTheFreshAnswerAgainstTheAPIServer(t *testing.T) {
	rs := ownershiptest.Object{Ref: resourceid.Ref{Group: "apps", Kind: "ReplicaSet", Namespace: "alpha", Name: "rs"}, APIVersion: "apps/v1", UID: "uid-rs", ResourceVersion: "1"}
	pod := ownershiptest.Object{Ref: resourceid.Ref{Kind: "Pod", Namespace: "alpha", Name: "p"}, APIVersion: "v1", UID: "uid-p", ResourceVersion: "10", Owners: []metav1.OwnerReference{rs.OwnerRef(true)}}
	updated := pod
	updated.ResourceVersion = "11"
	orphaned := updated
	orphaned.Owners = nil
	elsewhere := ownershiptest.Object{Ref: resourceid.Ref{Kind: "Pod", Namespace: "beta", Name: "q"}, APIVersion: "v1", UID: "uid-q", ResourceVersion: "20", Owners: []metav1.OwnerReference{rs.OwnerRef(true)}}
	sibling := ownershiptest.Object{Ref: resourceid.Ref{Kind: "Pod", Namespace: "alpha", Name: "sibling"}, APIVersion: "v1", UID: "uid-sibling", ResourceVersion: "30", Owners: []metav1.OwnerReference{rs.OwnerRef(true)}}
	partial := world(rs, updated)
	partial.Partial = true

	listed := world(rs, pod)
	behindList, listedWithSibling := world(rs, orphaned), world(rs, pod, sibling)
	q := ownershiptest.Queries{Refs: []resourceid.Ref{pod.Ref}, UIDs: []types.UID{"uid-rs"}, Domains: []ownership.Domain{ownership.InNamespace("alpha")}, Limits: []int{0}}
	for _, tc := range []struct {
		name  string
		now   ownershiptest.World
		cache func(now ownership.Session) ownership.Session
		// want is each question's outcome.
		want map[string]outcome
		// listed, when set, is what the list saw instead of the ReplicaSet
		// and the Pod before any change.
		listed *ownershiptest.World
	}{
		{"the Pod was updated, and the cache has the update", world(rs, updated), nil,
			map[string]outcome{"Object": confirmed, "OwnerCandidates": confirmed}, nil},
		{"the Pod lost its owner, and the cache knows", world(rs, orphaned), nil,
			map[string]outcome{"Object": confirmed, "OwnerCandidates": confirmed}, nil},
		{"the Pod was deleted, and the cache knows", world(rs), nil,
			map[string]outcome{"Object": confirmed, "OwnerCandidates": confirmed}, nil},
		// The list already saw the Pod orphaned; the cache, behind, still
		// had it as a candidate and catches up only on its third answer. The
		// disputed question is asked afresh after the objects settle, and
		// with nothing changed since the list the cache had merely been
		// behind.
		{"the cache was behind the list", world(rs, orphaned),
			func(now ownership.Session) ownership.Session { return lagging{now, begin(t, listed), new(int)} },
			map[string]outcome{"Object": caughtUp, "OwnerCandidates": caughtUp}, &behindList},
		// The cache's own absence is not evidence: the API server still has it.
		{"the cache keeps reporting the Pod absent", world(rs, pod),
			func(now ownership.Session) ownership.Session { return falseAbsence{now, pod.Ref, false, new(int)} },
			map[string]outcome{"Object": unresolved}, nil},
		// Right once asked again, with nothing changed: not a confirmed
		// change, whatever made the first answer wrong.
		{"the cache reports the Pod absent once", world(rs, pod),
			func(now ownership.Session) ownership.Session { return falseAbsence{now, pod.Ref, true, new(int)} },
			map[string]outcome{"Object": caughtUp}, nil},
		// A write that kept the Pod's owner doesn't excuse a missing
		// candidate.
		{"a status write, and the index drops the Pod", world(rs, updated),
			func(now ownership.Session) ownership.Session { return candidateDrop{now} },
			map[string]outcome{"Object": confirmed, "OwnerCandidates": unresolved}, nil},
		// Membership right, copy wrong.
		{"a candidate under a UID it doesn't have", world(rs, updated),
			func(now ownership.Session) ownership.Session { return inventedUID{now} },
			map[string]outcome{"Object": confirmed, "OwnerCandidates": unresolved}, nil},
		// A Pod outside the searched namespace, named in a search for alpha.
		{"a candidate outside the domain", world(rs, updated, elsewhere),
			func(now ownership.Session) ownership.Session {
				return extraCandidate{now, ownershiptest.Observe(elsewhere)}
			},
			map[string]outcome{"Object": confirmed, "OwnerCandidates": unresolved}, nil},
		// Every copy right, but one object named twice, with or without a
		// write since the list.
		{"a candidate returned twice", world(rs, pod),
			func(now ownership.Session) ownership.Session { return twice{now} },
			map[string]outcome{"OwnerCandidates": unresolved}, nil},
		{"a candidate returned twice after a write", world(rs, updated),
			func(now ownership.Session) ownership.Session { return twice{now} },
			map[string]outcome{"Object": confirmed, "OwnerCandidates": unresolved}, nil},
		// The sibling agreed at first and was never disputed; asked again,
		// the search drops it.
		{"a candidate that agreed, dropped when asked again", world(rs, updated, sibling),
			func(now ownership.Session) ownership.Session { return dropsLater{now, sibling.Ref, new(int)} },
			map[string]outcome{"Object": confirmed, "OwnerCandidates": unresolved}, &listedWithSibling},
		// Asked again, the search covers less than the expected answer.
		{"the fresh answer leaves more unread", world(rs, updated),
			func(now ownership.Session) ownership.Session { return laterGap{now, new(int)} },
			map[string]outcome{"Object": confirmed, "OwnerCandidates": unresolved}, nil},
		// A coverage difference fails even when it's gone by the time the
		// question is asked again: the caches are loaded before comparing.
		{"a coverage difference that passes", world(rs, updated),
			func(now ownership.Session) ownership.Session { return earlyGap{now, new(int)} },
			map[string]outcome{"Object": confirmed, "OwnerCandidates": unresolved}, nil},
		// Discovery went partial while the Pod changed: a coverage difference
		// beside an object one, never explained by the object.
		{"coverage and an object differ together", partial, nil,
			map[string]outcome{"Object": confirmed, "OwnerCandidates": unresolved}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := begin(t, tc.now)
			if tc.cache != nil {
				cache = tc.cache(cache)
			}
			from := listed
			if tc.listed != nil {
				from = *tc.listed
			}
			seen := map[string]bool{}
			for _, d := range ownershiptest.Differences(begin(t, from), cache, q) {
				question := "Object"
				if d.Candidates {
					question = "OwnerCandidates"
				}
				seen[question] = true
				want, expected := tc.want[question]
				if !expected {
					continue
				}
				if got := quick.recheck(d, listedOf(from), cache, apiOf(tc.now)); got != want {
					t.Errorf("%s: %v, want %v", d.Question, got, want)
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

// A gap without a kind is about the search, not about a kind it read, so it
// never excuses leaving out a listed object of a kind Radar reads.
func TestRecheckDoesNotLetAKindlessGapExcuseAnOmission(t *testing.T) {
	owner := ownershiptest.Object{Ref: resourceid.Ref{Group: "example.io", Kind: "ClusterWidget", Name: "parent"}, APIVersion: "example.io/v1", UID: "uid-parent", ResourceVersion: "1"}
	a := ownershiptest.Object{Ref: resourceid.Ref{Kind: "Node", Name: "a"}, APIVersion: "v1", UID: "uid-a", ResourceVersion: "10", Owners: []metav1.OwnerReference{owner.OwnerRef(false)}}
	sibling := ownershiptest.Object{Ref: resourceid.Ref{Kind: "Node", Name: "sibling"}, APIVersion: "v1", UID: "uid-sibling", ResourceVersion: "20", Owners: []metav1.OwnerReference{owner.OwnerRef(false)}}
	updated := a
	updated.ResourceVersion = "11"
	for _, tc := range []struct {
		name    string
		partial bool
		domain  ownership.Domain
	}{
		{"an empty namespace entry", false, ownership.Domain{Namespaces: []string{""}, ClusterScoped: true}},
		{"partial discovery", true, ownership.Domain{ClusterScoped: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listedWorld, now := world(owner, a, sibling), world(owner, updated, sibling)
			listedWorld.Partial, now.Partial = tc.partial, tc.partial
			cache := dropsLater{begin(t, now), sibling.Ref, new(int)}
			q := ownershiptest.Queries{UIDs: []types.UID{"uid-parent"}, Domains: []ownership.Domain{tc.domain}, Limits: []int{0}}
			diffs := ownershiptest.Differences(begin(t, listedWorld), cache, q)
			if len(diffs) != 1 || diffs[0].Coverage || len(diffs[0].WantGaps) == 0 || diffs[0].WantGaps[0].Kind != (resourceid.GroupKind{}) {
				t.Fatalf("scenario: want one object difference beside a kindless gap, got %v", diffs)
			}
			if got := quick.recheck(diffs[0], listedOf(listedWorld), cache, apiOf(now)); got != unresolved {
				t.Fatalf("%v, want unresolved", got)
			}
		})
	}
}

func TestRecheckResolutionNeedsOnlyTheOwnerToChange(t *testing.T) {
	rs := ownershiptest.Object{Ref: resourceid.Ref{Group: "apps", Kind: "ReplicaSet", Namespace: "alpha", Name: "rs"}, APIVersion: "apps/v1", UID: "uid-rs", ResourceVersion: "1"}
	pod := ownershiptest.Object{Ref: resourceid.Ref{Kind: "Pod", Namespace: "alpha", Name: "p"}, APIVersion: "v1", UID: "uid-p", ResourceVersion: "10", Owners: []metav1.OwnerReference{rs.OwnerRef(true)}}
	recreated := rs
	recreated.UID, recreated.ResourceVersion = "uid-rs-again", "2"
	listedWorld := world(rs, pod)
	listed := begin(t, listedWorld)
	dependent := listed.Object(pod.Ref).Object
	ref := dependent.OwnerReferences[0]
	unchanged := world(rs, pod)
	for _, tc := range []struct {
		name  string
		now   ownershiptest.World
		cache ownership.Session
		want  outcome
	}{
		{"the owner was recreated", world(recreated, pod), nil, confirmed},
		{"the owner was deleted", world(pod), nil, confirmed},
		{"an unchanged owner resolved wrongly", unchanged, falseAbsence{begin(t, unchanged), rs.Ref, false, new(int)}, unresolved},
		{"an unchanged owner resolved wrongly once", unchanged, falseAbsence{begin(t, unchanged), rs.Ref, true, new(int)}, caughtUp},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := tc.cache
			if cache == nil {
				cache = begin(t, tc.now)
			}
			a, b := ownership.ResolveOwner(listed, dependent, ref), ownership.ResolveOwner(cache, dependent, ref)
			if a.State == b.State {
				t.Fatal("scenario: the resolutions should differ")
			}
			if got := quick.recheckResolution(a, b, dependent, ref, listedOf(listedWorld), cache, apiOf(tc.now)); got != tc.want {
				t.Fatalf("%v, want %v (%v vs %v)", got, tc.want, a.State, b.State)
			}
		})
	}
}
