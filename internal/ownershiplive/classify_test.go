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
type candidateDrop struct{ ownership.ReadSession }

func (c candidateDrop) DependentCandidates(uid types.UID, d ownership.SearchScope) ownership.DependentCandidatesRead {
	r := c.ReadSession.DependentCandidates(uid, d)
	r.Candidates = nil
	return r
}

// inventedUID returns every candidate under a UID it doesn't have.
type inventedUID struct{ ownership.ReadSession }

func (c inventedUID) DependentCandidates(uid types.UID, d ownership.SearchScope) ownership.DependentCandidatesRead {
	r := c.ReadSession.DependentCandidates(uid, d)
	for i := range r.Candidates {
		r.Candidates[i].UID = "invented"
	}
	return r
}

// falseAbsence reports one object absent, always or only the first time.
type falseAbsence struct {
	ownership.ReadSession
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
	return f.ReadSession.Object(ref)
}

// extraCandidate adds an object to every search's candidates, as an index
// that ignores the scope would.
type extraCandidate struct {
	ownership.ReadSession
	extra ownership.Observation
}

func (e extraCandidate) DependentCandidates(uid types.UID, d ownership.SearchScope) ownership.DependentCandidatesRead {
	r := e.ReadSession.DependentCandidates(uid, d)
	r.Candidates = append(r.Candidates, e.extra)
	return r
}

// twice returns every candidate twice.
type twice struct{ ownership.ReadSession }

func (c twice) DependentCandidates(uid types.UID, d ownership.SearchScope) ownership.DependentCandidatesRead {
	r := c.ReadSession.DependentCandidates(uid, d)
	r.Candidates = append(r.Candidates, r.Candidates...)
	return r
}

// dropsLater answers the first search right, and leaves one object out of
// every later one.
type dropsLater struct {
	ownership.ReadSession
	drop  resourceid.Ref
	calls *int
}

func (l dropsLater) DependentCandidates(uid types.UID, d ownership.SearchScope) ownership.DependentCandidatesRead {
	r := l.ReadSession.DependentCandidates(uid, d)
	if *l.calls++; *l.calls > 1 {
		r.Candidates = slices.DeleteFunc(r.Candidates, func(c ownership.Observation) bool { return c.Ref == l.drop })
	}
	return r
}

// alwaysDrops leaves one object out of every search: an index that lost it.
type alwaysDrops struct {
	ownership.ReadSession
	drop resourceid.Ref
}

func (a alwaysDrops) DependentCandidates(uid types.UID, d ownership.SearchScope) ownership.DependentCandidatesRead {
	r := a.ReadSession.DependentCandidates(uid, d)
	r.Candidates = slices.DeleteFunc(r.Candidates, func(c ownership.Observation) bool { return c.Ref == a.drop })
	return r
}

func (a alwaysDrops) RecordingManagers(ref resourceid.Ref) ownership.RecordingManagersRead {
	r := a.ReadSession.RecordingManagers(ref)
	r.Managers = slices.DeleteFunc(r.Managers, func(o ownership.Observation) bool { return o.Ref == a.drop })
	r.Remote = slices.DeleteFunc(r.Remote, func(m resourceid.Ref) bool { return m == a.drop })
	r.Unreadable = slices.DeleteFunc(r.Unreadable, func(m resourceid.Ref) bool { return m == a.drop })
	return r
}

// misleadsLater leaves b out of the first two searches of each kind, and
// then returns b under a UID it doesn't have, or returns b and leaves a out.
type misleadsLater struct {
	ownership.ReadSession
	a, b  resourceid.Ref
	lose  bool
	calls map[string]int
}

func (m misleadsLater) DependentCandidates(uid types.UID, d ownership.SearchScope) ownership.DependentCandidatesRead {
	r := m.ReadSession.DependentCandidates(uid, d)
	r.Candidates = m.mislead("DependentCandidates", r.Candidates)
	return r
}

func (m misleadsLater) RecordingManagers(ref resourceid.Ref) ownership.RecordingManagersRead {
	r := m.ReadSession.RecordingManagers(ref)
	r.Managers = m.mislead("RecordingManagers", r.Managers)
	return r
}

func (m misleadsLater) mislead(question string, found []ownership.Observation) []ownership.Observation {
	m.calls[question]++
	out := slices.Clone(found)
	switch {
	case m.calls[question] <= 2:
		return slices.DeleteFunc(out, func(o ownership.Observation) bool { return o.Ref == m.b })
	case m.lose:
		return slices.DeleteFunc(out, func(o ownership.Observation) bool { return o.Ref == m.a })
	}
	for i := range out {
		if out[i].Ref == m.b {
			out[i].UID = "never-existed"
		}
	}
	return out
}

// earlyGap adds a gap to the first search only.
type earlyGap struct {
	ownership.ReadSession
	calls *int
}

func (e earlyGap) DependentCandidates(uid types.UID, d ownership.SearchScope) ownership.DependentCandidatesRead {
	r := e.ReadSession.DependentCandidates(uid, d)
	if *e.calls++; *e.calls == 1 {
		r.Gaps = append(r.Gaps, ownership.Gap{Kind: resourceid.GroupKind{Kind: "Pod"}, Namespace: "beta", Reason: "kind_sync_pending"})
	}
	return r
}

// laterGap adds a gap to every search after the first.
type laterGap struct {
	ownership.ReadSession
	calls *int
}

func (l laterGap) DependentCandidates(uid types.UID, d ownership.SearchScope) ownership.DependentCandidatesRead {
	r := l.ReadSession.DependentCandidates(uid, d)
	if *l.calls++; *l.calls > 1 {
		r.Gaps = append(r.Gaps, ownership.Gap{Kind: resourceid.GroupKind{Kind: "Pod"}, Namespace: "beta", Reason: "kind_sync_pending"})
	}
	return r
}

// lagging answers from an older view of the cluster until it has been asked
// twice, as a cache does while its watch catches up.
type lagging struct {
	ownership.ReadSession
	old   ownership.ReadSession
	calls *int
}

func (l lagging) view() ownership.ReadSession {
	*l.calls++
	if *l.calls <= 2 {
		return l.old
	}
	return l.ReadSession
}

func (l lagging) Object(ref resourceid.Ref) ownership.ObjectRead { return l.view().Object(ref) }

func (l lagging) DependentCandidates(uid types.UID, d ownership.SearchScope) ownership.DependentCandidatesRead {
	return l.view().DependentCandidates(uid, d)
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

func begin(t *testing.T, w ownershiptest.World) ownership.ReadSession {
	t.Helper()
	s, err := ownershiptest.Scan(w).Begin()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRecheckJudgesTheFreshAnswerAgainstTheAPIServer(t *testing.T) {
	rs := ownershiptest.Object{Ref: resourceid.Ref{Group: "apps", Kind: "ReplicaSet", Namespace: "alpha", Name: "rs"}, APIVersion: "apps/v1", UID: "uid-rs", ResourceVersion: "1"}
	pod := ownershiptest.Object{Ref: resourceid.Ref{Kind: "Pod", Namespace: "alpha", Name: "p"}, APIVersion: "v1", UID: "uid-p", ResourceVersion: "10", OwnerReferences: []metav1.OwnerReference{rs.OwnerRef(true)}}
	updated := pod
	updated.ResourceVersion = "11"
	orphaned := updated
	orphaned.OwnerReferences = nil
	elsewhere := ownershiptest.Object{Ref: resourceid.Ref{Kind: "Pod", Namespace: "beta", Name: "q"}, APIVersion: "v1", UID: "uid-q", ResourceVersion: "20", OwnerReferences: []metav1.OwnerReference{rs.OwnerRef(true)}}
	sibling := ownershiptest.Object{Ref: resourceid.Ref{Kind: "Pod", Namespace: "alpha", Name: "sibling"}, APIVersion: "v1", UID: "uid-sibling", ResourceVersion: "30", OwnerReferences: []metav1.OwnerReference{rs.OwnerRef(true)}}
	stranger := ownershiptest.Object{Ref: resourceid.Ref{Kind: "Pod", Namespace: "alpha", Name: "stranger"}, APIVersion: "v1", UID: "uid-stranger", ResourceVersion: "40"}
	adopted := stranger
	adopted.ResourceVersion, adopted.OwnerReferences = "41", []metav1.OwnerReference{rs.OwnerRef(true)}
	partial := world(rs, updated)
	partial.Partial = true

	listed := world(rs, pod)
	behindList, listedWithSibling, listedWithStranger := world(rs, orphaned), world(rs, pod, sibling), world(rs, pod, stranger)
	q := ownershiptest.Queries{Refs: []resourceid.Ref{pod.Ref}, UIDs: []types.UID{"uid-rs"}, Scopes: []ownership.SearchScope{ownership.InNamespace("alpha")}}
	for _, tc := range []struct {
		name  string
		now   ownershiptest.World
		cache func(now ownership.ReadSession) ownership.ReadSession
		// want is each question's outcome.
		want map[string]outcome
		// listed, when set, is what the list saw instead of the ReplicaSet
		// and the Pod before any change.
		listed *ownershiptest.World
	}{
		{"the Pod was updated, and the cache has the update", world(rs, updated), nil,
			map[string]outcome{"Object": confirmed, "DependentCandidates": confirmed}, nil},
		{"the Pod lost its owner, and the cache knows", world(rs, orphaned), nil,
			map[string]outcome{"Object": confirmed, "DependentCandidates": confirmed}, nil},
		{"the Pod was deleted, and the cache knows", world(rs), nil,
			map[string]outcome{"Object": confirmed, "DependentCandidates": confirmed}, nil},
		// The list already saw the Pod orphaned; the cache, behind, still
		// had it as a candidate and catches up only on its third answer. The
		// disputed question is asked afresh after the objects settle, and
		// with nothing changed since the list the cache had merely been
		// behind.
		{"the cache was behind the list", world(rs, orphaned),
			func(now ownership.ReadSession) ownership.ReadSession { return lagging{now, begin(t, listed), new(int)} },
			map[string]outcome{"Object": caughtUp, "DependentCandidates": caughtUp}, &behindList},
		// The cache's own absence is not evidence: the API server still has it.
		{"the cache keeps reporting the Pod absent", world(rs, pod),
			func(now ownership.ReadSession) ownership.ReadSession {
				return falseAbsence{now, pod.Ref, false, new(int)}
			},
			map[string]outcome{"Object": unresolved}, nil},
		// Right once asked again, with nothing changed: not a confirmed
		// change, whatever made the first answer wrong.
		{"the cache reports the Pod absent once", world(rs, pod),
			func(now ownership.ReadSession) ownership.ReadSession {
				return falseAbsence{now, pod.Ref, true, new(int)}
			},
			map[string]outcome{"Object": caughtUp}, nil},
		// A write that kept the Pod's owner doesn't excuse a missing
		// candidate.
		{"a status write, and the index drops the Pod", world(rs, updated),
			func(now ownership.ReadSession) ownership.ReadSession { return candidateDrop{now} },
			map[string]outcome{"Object": confirmed, "DependentCandidates": unresolved}, nil},
		// Membership right, copy wrong.
		{"a candidate under a UID it doesn't have", world(rs, updated),
			func(now ownership.ReadSession) ownership.ReadSession { return inventedUID{now} },
			map[string]outcome{"Object": confirmed, "DependentCandidates": unresolved}, nil},
		// A Pod outside the searched namespace, named in a search for alpha.
		{"a candidate outside the scope", world(rs, updated, elsewhere),
			func(now ownership.ReadSession) ownership.ReadSession {
				return extraCandidate{now, ownershiptest.Observe(elsewhere)}
			},
			map[string]outcome{"Object": confirmed, "DependentCandidates": unresolved}, nil},
		// Every copy right, but one object named twice, with or without a
		// write since the list.
		{"a candidate returned twice", world(rs, pod),
			func(now ownership.ReadSession) ownership.ReadSession { return twice{now} },
			map[string]outcome{"DependentCandidates": unresolved}, nil},
		{"a candidate returned twice after a write", world(rs, updated),
			func(now ownership.ReadSession) ownership.ReadSession { return twice{now} },
			map[string]outcome{"Object": confirmed, "DependentCandidates": unresolved}, nil},
		// The sibling agreed at first and was never disputed; asked again,
		// the search drops it.
		{"a candidate that agreed, dropped when asked again", world(rs, updated, sibling),
			func(now ownership.ReadSession) ownership.ReadSession { return dropsLater{now, sibling.Ref, new(int)} },
			map[string]outcome{"Object": confirmed, "DependentCandidates": unresolved}, &listedWithSibling},
		// The stranger named nothing when listed; it names the owner now,
		// and the search never finds it.
		{"a listed object that came to name the owner, never found", world(rs, updated, adopted),
			func(now ownership.ReadSession) ownership.ReadSession { return alwaysDrops{now, adopted.Ref} },
			map[string]outcome{"Object": confirmed, "DependentCandidates": unresolved}, &listedWithStranger},
		// Asked again for the stranger it owes, the search returns it wrong,
		// or at another's expense.
		{"an owed object returned under a UID it doesn't have", world(rs, updated, adopted),
			func(now ownership.ReadSession) ownership.ReadSession {
				return misleadsLater{now, pod.Ref, adopted.Ref, false, map[string]int{}}
			},
			map[string]outcome{"Object": confirmed, "DependentCandidates": unresolved}, &listedWithStranger},
		{"an owed object returned at another's expense", world(rs, updated, adopted),
			func(now ownership.ReadSession) ownership.ReadSession {
				return misleadsLater{now, pod.Ref, adopted.Ref, true, map[string]int{}}
			},
			map[string]outcome{"Object": confirmed, "DependentCandidates": unresolved}, &listedWithStranger},
		// Asked again, the search covers less than the expected answer.
		{"the fresh answer leaves more unread", world(rs, updated),
			func(now ownership.ReadSession) ownership.ReadSession { return laterGap{now, new(int)} },
			map[string]outcome{"Object": confirmed, "DependentCandidates": unresolved}, nil},
		// A coverage difference fails even when it's gone by the time the
		// question is asked again: the caches are loaded before comparing.
		{"a coverage difference that passes", world(rs, updated),
			func(now ownership.ReadSession) ownership.ReadSession { return earlyGap{now, new(int)} },
			map[string]outcome{"Object": confirmed, "DependentCandidates": unresolved}, nil},
		// Discovery went partial while the Pod changed: a coverage difference
		// beside an object one, never explained by the object.
		{"coverage and an object differ together", partial, nil,
			map[string]outcome{"Object": confirmed, "DependentCandidates": unresolved}, nil},
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
				if d.Asked == ownershiptest.AskedDependentCandidates {
					question = "DependentCandidates"
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
	a := ownershiptest.Object{Ref: resourceid.Ref{Kind: "Node", Name: "a"}, APIVersion: "v1", UID: "uid-a", ResourceVersion: "10", OwnerReferences: []metav1.OwnerReference{owner.OwnerRef(false)}}
	sibling := ownershiptest.Object{Ref: resourceid.Ref{Kind: "Node", Name: "sibling"}, APIVersion: "v1", UID: "uid-sibling", ResourceVersion: "20", OwnerReferences: []metav1.OwnerReference{owner.OwnerRef(false)}}
	updated := a
	updated.ResourceVersion = "11"
	for _, tc := range []struct {
		name    string
		partial bool
		scope   ownership.SearchScope
	}{
		{"an empty namespace entry", false, ownership.SearchScope{Namespaces: []string{""}, IncludeClusterScoped: true}},
		{"partial discovery", true, ownership.SearchScope{IncludeClusterScoped: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listedWorld, now := world(owner, a, sibling), world(owner, updated, sibling)
			listedWorld.Partial, now.Partial = tc.partial, tc.partial
			cache := dropsLater{begin(t, now), sibling.Ref, new(int)}
			q := ownershiptest.Queries{UIDs: []types.UID{"uid-parent"}, Scopes: []ownership.SearchScope{tc.scope}}
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
	pod := ownershiptest.Object{Ref: resourceid.Ref{Kind: "Pod", Namespace: "alpha", Name: "p"}, APIVersion: "v1", UID: "uid-p", ResourceVersion: "10", OwnerReferences: []metav1.OwnerReference{rs.OwnerRef(true)}}
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
		cache ownership.ReadSession
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

// recorderDrop drops every recorder: a broken membership index.
type recorderDrop struct{ ownership.ReadSession }

func (c recorderDrop) RecordingManagers(ref resourceid.Ref) ownership.RecordingManagersRead {
	r := c.ReadSession.RecordingManagers(ref)
	r.Managers = nil
	return r
}

// recorderTwice returns every recorder twice.
type recorderTwice struct{ ownership.ReadSession }

func (c recorderTwice) RecordingManagers(ref resourceid.Ref) ownership.RecordingManagersRead {
	r := c.ReadSession.RecordingManagers(ref)
	r.Managers = append(r.Managers, r.Managers...)
	return r
}

// recorderUnreadable reports every recorder as a manager whose list can't be
// read instead.
type recorderUnreadable struct{ ownership.ReadSession }

func (c recorderUnreadable) RecordingManagers(ref resourceid.Ref) ownership.RecordingManagersRead {
	r := c.ReadSession.RecordingManagers(ref)
	for _, o := range r.Managers {
		r.Unreadable = append(r.Unreadable, o.Ref)
	}
	r.Managers = nil
	return r
}

// extraRecordingManager adds a manager to every search's managers, as an index
// that kept a stale entry would.
type extraRecordingManager struct {
	ownership.ReadSession
	extra ownership.Observation
}

func (e extraRecordingManager) RecordingManagers(ref resourceid.Ref) ownership.RecordingManagersRead {
	r := e.ReadSession.RecordingManagers(ref)
	r.Managers = append(r.Managers, e.extra)
	return r
}

// laterManagerGap adds a gap to every search for recording managers after the first.
type laterManagerGap struct {
	ownership.ReadSession
	calls *int
}

func (l laterManagerGap) RecordingManagers(ref resourceid.Ref) ownership.RecordingManagersRead {
	r := l.ReadSession.RecordingManagers(ref)
	if *l.calls++; *l.calls > 1 {
		r.Gaps = append(r.Gaps, ownership.Gap{Kind: ownership.ArgoApplication, Namespace: "argocd", Reason: "kind_sync_pending"})
	}
	return r
}

// claimsRecorded reports a manager whose list can't be read as listing
// nothing.
type claimsRecorded struct{ ownership.ReadSession }

func (c claimsRecorded) Members(m resourceid.Ref) ownership.MembersRead {
	r := c.ReadSession.Members(m)
	if r.Manager.State == ownership.Present && r.Membership == ownership.MembersUnreadable {
		r.Membership, r.Members = ownership.MembersRecorded, []resourceid.Ref{}
	}
	return r
}

func (c claimsRecorded) Membership(m resourceid.Ref) ownership.MembersRead {
	r := c.Members(m)
	r.Members = nil
	return r
}

// managerDropsLater answers the first search for recording managers right, and
// leaves one manager out of every later one.
type managerDropsLater struct {
	ownership.ReadSession
	drop  resourceid.Ref
	calls *int
}

func (l managerDropsLater) RecordingManagers(ref resourceid.Ref) ownership.RecordingManagersRead {
	r := l.ReadSession.RecordingManagers(ref)
	if *l.calls++; *l.calls > 1 {
		r.Managers = slices.DeleteFunc(r.Managers, func(o ownership.Observation) bool { return o.Ref == l.drop })
		r.Remote = slices.DeleteFunc(r.Remote, func(m resourceid.Ref) bool { return m == l.drop })
	}
	return r
}

// remoteAsRecording reports a remote manager as recording the object.
type remoteAsRecording struct {
	ownership.ReadSession
	remote ownership.Observation
}

func (e remoteAsRecording) RecordingManagers(ref resourceid.Ref) ownership.RecordingManagersRead {
	r := e.ReadSession.RecordingManagers(ref)
	if slices.Contains(r.Remote, e.remote.Ref) {
		r.Remote = slices.DeleteFunc(r.Remote, func(m resourceid.Ref) bool { return m == e.remote.Ref })
		r.Managers = append(r.Managers, e.remote)
	}
	return r
}

// memberDrop leaves every manager's members out of its inventory.
type memberDrop struct{ ownership.ReadSession }

func (c memberDrop) Members(m resourceid.Ref) ownership.MembersRead {
	r := c.ReadSession.Members(m)
	if r.Members != nil {
		r.Members = []resourceid.Ref{}
	}
	return r
}

// laggingMembership answers each kind of question from an older view the
// first time it is asked.
type laggingMembership struct {
	ownership.ReadSession
	old   ownership.ReadSession
	calls map[string]int
}

func (l laggingMembership) view(question string) ownership.ReadSession {
	l.calls[question]++
	if l.calls[question] == 1 {
		return l.old
	}
	return l.ReadSession
}

func (l laggingMembership) Object(ref resourceid.Ref) ownership.ObjectRead {
	return l.view("Object").Object(ref)
}

func (l laggingMembership) RecordingManagers(ref resourceid.Ref) ownership.RecordingManagersRead {
	return l.view("RecordingManagers").RecordingManagers(ref)
}

func (l laggingMembership) Members(m resourceid.Ref) ownership.MembersRead {
	return l.view("Members").Members(m)
}

func (l laggingMembership) Membership(m resourceid.Ref) ownership.MembersRead {
	return l.view("Members").Membership(m)
}

func TestRecheckJudgesMembershipAgainstTheAPIServer(t *testing.T) {
	cm := ownershiptest.Object{Ref: resourceid.Ref{Kind: "ConfigMap", Namespace: "shop", Name: "cfg"}, APIVersion: "v1", UID: "uid-cfg", ResourceVersion: "5"}
	app := ownershiptest.Object{Ref: resourceid.Ref{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "shop"}, APIVersion: "argoproj.io/v1alpha1", UID: "uid-app", ResourceVersion: "10",
		Membership: ownership.MembersRecorded, Members: []resourceid.Ref{cm.Ref}, DestinationNamespace: "shop"}
	dropped := app
	dropped.ResourceVersion, dropped.Members = "11", []resourceid.Ref{}
	resynced := app
	resynced.ResourceVersion = "12"
	unreadable := app
	unreadable.ResourceVersion, unreadable.Membership, unreadable.Members = "13", ownership.MembersUnreadable, nil

	other := ownershiptest.Object{Ref: resourceid.Ref{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "other"}, APIVersion: "argoproj.io/v1alpha1", UID: "uid-other", ResourceVersion: "20",
		Membership: ownership.MembersRecorded, Members: []resourceid.Ref{}, DestinationNamespace: "shop"}

	sibling := app
	sibling.Ref.Name, sibling.UID = "shop-too", "uid-shop-too"
	remote := app
	remote.Ref.Name, remote.UID, remote.Membership = "shop-prod", "uid-shop-prod", ownership.MembersRemote

	listsNothing := sibling
	listsNothing.ResourceVersion, listsNothing.Members = "9", []resourceid.Ref{}
	remoteResynced := remote
	remoteResynced.ResourceVersion = "14"
	siblingRemote, siblingUnreadable := sibling, sibling
	siblingRemote.ResourceVersion, siblingRemote.Membership = "15", ownership.MembersRemote
	siblingUnreadable.ResourceVersion, siblingUnreadable.Membership, siblingUnreadable.Members = "16", ownership.MembersUnreadable, nil

	q := ownershiptest.Queries{Refs: []resourceid.Ref{cm.Ref}, Managers: []resourceid.Ref{app.Ref, sibling.Ref, remote.Ref}}
	for _, tc := range []struct {
		name   string
		listed ownershiptest.World
		now    ownershiptest.World
		cache  func(now ownership.ReadSession) ownership.ReadSession
		want   map[ownershiptest.Asked]outcome
	}{
		{"the Application stopped listing the ConfigMap", world(cm, app), world(cm, dropped), nil,
			map[ownershiptest.Asked]outcome{ownershiptest.AskedRecordingManagers: confirmed, ownershiptest.AskedMembers: confirmed}},
		{"the Application's list went unreadable", world(cm, app), world(cm, unreadable), nil,
			map[ownershiptest.Asked]outcome{ownershiptest.AskedRecordingManagers: confirmed, ownershiptest.AskedMembers: confirmed}},
		{"the cache was behind the list", world(cm, dropped), world(cm, dropped),
			func(now ownership.ReadSession) ownership.ReadSession {
				return laggingMembership{now, begin(t, world(cm, app)), map[string]int{}}
			},
			map[ownershiptest.Asked]outcome{ownershiptest.AskedRecordingManagers: caughtUp, ownershiptest.AskedMembers: caughtUp}},
		// A status write that kept the member doesn't excuse a missing
		// recorder.
		{"a status write, and the index drops the recorder", world(cm, app), world(cm, resynced),
			func(now ownership.ReadSession) ownership.ReadSession { return recorderDrop{now} },
			map[ownershiptest.Asked]outcome{ownershiptest.AskedRecordingManagers: unresolved, ownershiptest.AskedMembers: confirmed}},
		{"a recorder returned twice", world(cm, app), world(cm, app),
			func(now ownership.ReadSession) ownership.ReadSession { return recorderTwice{now} },
			map[ownershiptest.Asked]outcome{ownershiptest.AskedRecordingManagers: unresolved}},
		{"a recording manager reported unreadable", world(cm, app), world(cm, resynced),
			func(now ownership.ReadSession) ownership.ReadSession { return recorderUnreadable{now} },
			map[ownershiptest.Asked]outcome{ownershiptest.AskedRecordingManagers: unresolved, ownershiptest.AskedMembers: confirmed}},
		{"a recorder that doesn't list the object", world(cm, app, other), world(cm, app, other),
			func(now ownership.ReadSession) ownership.ReadSession {
				return extraRecordingManager{now, ownershiptest.Observe(other)}
			},
			map[ownershiptest.Asked]outcome{ownershiptest.AskedRecordingManagers: unresolved}},
		{"the fresh search for recording managers leaves more unread", world(cm, app), world(cm, resynced),
			func(now ownership.ReadSession) ownership.ReadSession { return laterManagerGap{now, new(int)} },
			map[ownershiptest.Asked]outcome{ownershiptest.AskedRecordingManagers: unresolved, ownershiptest.AskedMembers: confirmed}},
		{"an unreadable list reported as recorded", world(cm, app), world(cm, unreadable),
			func(now ownership.ReadSession) ownership.ReadSession { return claimsRecorded{now} },
			map[ownershiptest.Asked]outcome{ownershiptest.AskedRecordingManagers: confirmed, ownershiptest.AskedMembers: unresolved}},
		// The sibling agreed at first and was never disputed; asked again,
		// the search drops it.
		{"a recorder that agreed, dropped when asked again", world(cm, app, sibling), world(cm, resynced, sibling),
			func(now ownership.ReadSession) ownership.ReadSession {
				return managerDropsLater{now, sibling.Ref, new(int)}
			},
			map[ownershiptest.Asked]outcome{ownershiptest.AskedRecordingManagers: unresolved, ownershiptest.AskedMembers: confirmed}},
		{"a remote manager listing the object", world(cm, app, remote), world(cm, resynced, remote), nil,
			map[ownershiptest.Asked]outcome{ownershiptest.AskedRecordingManagers: confirmed, ownershiptest.AskedMembers: confirmed}},
		{"a remote manager reported as a recorder", world(cm, app, remote), world(cm, resynced, remote),
			func(now ownership.ReadSession) ownership.ReadSession {
				return remoteAsRecording{now, ownershiptest.Observe(remote)}
			},
			map[ownershiptest.Asked]outcome{ownershiptest.AskedRecordingManagers: unresolved, ownershiptest.AskedMembers: confirmed}},
		{"a remote manager dropped when asked again", world(cm, app, remote), world(cm, resynced, remote),
			func(now ownership.ReadSession) ownership.ReadSession {
				return managerDropsLater{now, remote.Ref, new(int)}
			},
			map[ownershiptest.Asked]outcome{ownershiptest.AskedRecordingManagers: unresolved, ownershiptest.AskedMembers: confirmed}},
		{"a remote inventory missing its member", world(cm, remote), world(cm, remoteResynced),
			func(now ownership.ReadSession) ownership.ReadSession { return memberDrop{now} },
			map[ownershiptest.Asked]outcome{ownershiptest.AskedMembers: unresolved}},
		// The sibling listed nothing when listed; it lists the ConfigMap now,
		// in each way, and the search never finds it.
		{"a listed manager that came to record the object, never found", world(cm, app, listsNothing), world(cm, resynced, sibling),
			func(now ownership.ReadSession) ownership.ReadSession { return alwaysDrops{now, sibling.Ref} },
			map[ownershiptest.Asked]outcome{ownershiptest.AskedRecordingManagers: unresolved, ownershiptest.AskedMembers: confirmed}},
		{"a listed manager that came to list it remotely, never found", world(cm, app, listsNothing), world(cm, resynced, siblingRemote),
			func(now ownership.ReadSession) ownership.ReadSession { return alwaysDrops{now, sibling.Ref} },
			map[ownershiptest.Asked]outcome{ownershiptest.AskedRecordingManagers: unresolved, ownershiptest.AskedMembers: confirmed}},
		{"a listed manager whose list became unreadable, never found", world(cm, app, listsNothing), world(cm, resynced, siblingUnreadable),
			func(now ownership.ReadSession) ownership.ReadSession { return alwaysDrops{now, sibling.Ref} },
			map[ownershiptest.Asked]outcome{ownershiptest.AskedRecordingManagers: unresolved, ownershiptest.AskedMembers: confirmed}},
		{"an owed manager returned under a UID it doesn't have", world(cm, app, listsNothing), world(cm, resynced, sibling),
			func(now ownership.ReadSession) ownership.ReadSession {
				return misleadsLater{now, app.Ref, sibling.Ref, false, map[string]int{}}
			},
			map[ownershiptest.Asked]outcome{ownershiptest.AskedRecordingManagers: unresolved, ownershiptest.AskedMembers: confirmed}},
		{"an owed manager returned at another's expense", world(cm, app, listsNothing), world(cm, resynced, sibling),
			func(now ownership.ReadSession) ownership.ReadSession {
				return misleadsLater{now, app.Ref, sibling.Ref, true, map[string]int{}}
			},
			map[ownershiptest.Asked]outcome{ownershiptest.AskedRecordingManagers: unresolved, ownershiptest.AskedMembers: confirmed}},
		{"an inventory missing its member", world(cm, app), world(cm, resynced),
			func(now ownership.ReadSession) ownership.ReadSession { return memberDrop{now} },
			map[ownershiptest.Asked]outcome{ownershiptest.AskedRecordingManagers: confirmed, ownershiptest.AskedMembers: unresolved}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := begin(t, tc.now)
			if tc.cache != nil {
				cache = tc.cache(cache)
			}
			seen := map[ownershiptest.Asked]bool{}
			for _, d := range ownershiptest.Differences(begin(t, tc.listed), cache, q) {
				if d.Asked == ownershiptest.AskedObject {
					continue
				}
				seen[d.Asked] = true
				want, expected := tc.want[d.Asked]
				if !expected {
					t.Errorf("unexpected difference %s", d)
					continue
				}
				if got := quick.recheck(d, listedOf(tc.listed), cache, apiOf(tc.now)); got != want {
					t.Errorf("%s: %v, want %v", d.Question, got, want)
				}
			}
			for asked := range tc.want {
				if !seen[asked] {
					t.Errorf("scenario: no difference for question %d", asked)
				}
			}
		})
	}
}
