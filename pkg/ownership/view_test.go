package ownership_test

import (
	"fmt"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// access is a caller who may read the namespaces listed (every one when nil)
// and every kind but those denied or undecided.
type access struct {
	namespaces []string
	denied     []resourceid.GroupKind
	undecided  []resourceid.GroupKind
}

func (a access) CanRead(gk resourceid.GroupKind, namespace string) ownership.Decision {
	switch {
	case slices.Contains(a.undecided, gk):
		return ownership.Undecided
	case slices.Contains(a.denied, gk), namespace != "" && a.namespaces != nil && !slices.Contains(a.namespaces, namespace):
		return ownership.Denied
	}
	return ownership.Allowed
}

func (a access) Namespaces() ([]string, bool) { return a.namespaces, a.namespaces == nil }

func view(t *testing.T, w ownershiptest.World, a ownership.Access) *ownership.View {
	t.Helper()
	s, err := ownershiptest.Scan(w).Begin()
	if err != nil {
		t.Fatal(err)
	}
	return ownership.NewView(s, a)
}

func at(group, kind, namespace, name string) resourceid.Ref {
	return resourceid.Ref{Group: group, Kind: kind, Namespace: namespace, Name: name}
}

var (
	replicaSets = resourceid.GroupKind{Group: "apps", Kind: "ReplicaSet"}
	pods        = resourceid.GroupKind{Kind: "Pod"}
	configMaps  = resourceid.GroupKind{Kind: "ConfigMap"}
)

func TestViewOwnerIsTheControllerOrTheOnlyReference(t *testing.T) {
	w := ownershiptest.Fixture()
	rs := at("apps", "ReplicaSet", "alpha", "web-1")
	for _, tc := range []struct {
		name    string
		access  ownership.Access
		subject resourceid.Ref
		index   int
		state   ownership.OwnerState
		ref     resourceid.Ref
		several bool
		gap     ownership.Gap
	}{
		{"a controller", ownership.Unrestricted(), at("", "Pod", "alpha", "web-1-a"), 0, ownership.OwnerFound, rs, false, ownership.Gap{}},
		{"the controller of two", ownership.Unrestricted(), at("", "Pod", "alpha", "web-1-b"), 0, ownership.OwnerFound, rs, false, ownership.Gap{}},
		{"several without a controller", ownership.Unrestricted(), at("", "ConfigMap", "alpha", "shared"), -1, 0, resourceid.Ref{}, true, ownership.Gap{}},
		{"none", ownership.Unrestricted(), at("apps", "Deployment", "alpha", "web"), -1, 0, resourceid.Ref{}, false, ownership.Gap{}},
		{"an only reference to a later incarnation", ownership.Unrestricted(), at("", "Pod", "alpha", "replaced"), 0, ownership.OwnerReplaced, at("apps", "ReplicaSet", "alpha", "web-old"), false, ownership.Gap{}},
		{"an only reference to nothing", ownership.Unrestricted(), at("", "Pod", "alpha", "dangling"), 0, ownership.OwnerGone, at("apps", "ReplicaSet", "alpha", "gone"), false, ownership.Gap{}},
		{"a namespaced owner of a cluster-scoped object", ownership.Unrestricted(), at("", "Node", "", "n2"), 0, ownership.OwnerUnresolvable, resourceid.Ref{}, false, ownership.Gap{}},
		{"a withheld controller never gives way to the ConfigMap", access{denied: []resourceid.GroupKind{replicaSets}}, at("", "Pod", "alpha", "web-1-b"), 0, ownership.OwnerWithheld, rs, false, ownership.Gap{}},
		{"an undecided check", access{undecided: []resourceid.GroupKind{replicaSets}}, at("", "Pod", "alpha", "web-1-b"), 0, ownership.OwnerUnknown, rs, false,
			ownership.Gap{Kind: replicaSets, Namespace: "alpha", Reason: readreason.UserPermissionCheckFailed}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := view(t, w, tc.access).Owner(tc.subject)
			if !got.Subject.Present() {
				t.Fatalf("subject = %v %v", got.Subject.Access, got.Subject.Read.State)
			}
			if index := referenceIndex(got); index != tc.index || got.Several != tc.several || got.Resolution.State != tc.state || got.Resolution.Ref != tc.ref || got.Resolution.Gap != tc.gap {
				t.Fatalf("Owner = index %d several %v %v %v %+v; want index %d several %v %v %v %+v",
					index, got.Several, got.Resolution.State, got.Resolution.Ref, got.Resolution.Gap, tc.index, tc.several, tc.state, tc.ref, tc.gap)
			}
		})
	}
}

func TestViewSaysNothingOfAnObjectTheCallerMayNotRead(t *testing.T) {
	v := view(t, ownershiptest.Fixture(), access{denied: []resourceid.GroupKind{pods}})
	pod := at("", "Pod", "alpha", "web-1-a")
	if got := v.Owner(pod); got.Subject.Access != ownership.Denied || got.Subject.Read.State != ownership.Unavailable || got.Subject.Read.Object.UID != "" || got.Reference != nil {
		t.Fatalf("Owner of a Pod the caller may not read = %+v", got)
	}
	if got := v.Children(pod); got.Subject.Present() || got.Children != nil || got.Gaps != nil || got.Complete() {
		t.Fatalf("Children = %+v complete=%v", got, got.Complete())
	}
	unsure := view(t, ownershiptest.Fixture(), access{undecided: []resourceid.GroupKind{pods}})
	if got := unsure.Children(pod); got.Subject.Present() || got.Complete() {
		t.Fatalf("Children when the caller's access couldn't be checked = %+v complete=%v", got, got.Complete())
	}
	if got := v.ControllerChain(pod); got.Subject.Present() || len(got.Chain) != 0 || got.UnfollowedReference != nil {
		t.Fatalf("ControllerChain = %+v", got)
	}
}

// An object the caller fetched from the API answers for itself when Radar
// doesn't collect its kind; its owners are still read from the caches.
func TestViewSeedsAnObjectTheCachesDoNotHold(t *testing.T) {
	w := ownershiptest.Fixture()
	w.Collection[configMaps] = ownershiptest.Collection{State: ownershiptest.NotCollected}
	fetched := at("", "ConfigMap", "alpha", "versioned")
	var o ownershiptest.Object
	for _, x := range w.Objects {
		if x.Ref == fetched {
			o = x
		}
	}
	observation := ownership.Observation{Ref: o.Ref, UID: o.UID, OwnerReferences: o.OwnerReferences}

	v := view(t, w, ownership.Unrestricted())
	if got := v.Owner(fetched); got.Subject.Present() {
		t.Fatalf("scenario: ConfigMaps must be uncollected, got %v", got.Subject.Read.State)
	}
	v = view(t, w, ownership.Unrestricted())
	v.Seed(observation)
	if got := v.Owner(fetched); got.Resolution.State != ownership.OwnerFound || got.Resolution.Ref != at("example.io", "Widget", "alpha", "w1") {
		t.Fatalf("Owner of the fetched ConfigMap = %v %v", got.Resolution.State, got.Resolution.Ref)
	}

	// A read that holds the object stands.
	held := view(t, ownershiptest.Fixture(), ownership.Unrestricted())
	stale := observation
	stale.UID = "uid-of-another-incarnation"
	held.Seed(stale)
	if got := held.Owner(fetched); got.Subject.Read.Object.UID != o.UID {
		t.Fatalf("a seed replaced the held read: %v", got.Subject.Read.Object.UID)
	}

	// An Event stays excluded.
	event := ownership.Observation{Ref: at("", "Event", "alpha", "e1"), UID: "uid-alpha-e1"}
	events := view(t, ownershiptest.Fixture(), ownership.Unrestricted())
	events.Seed(event)
	if got := events.Owner(event.Ref); got.Subject.Read.State != ownership.Excluded {
		t.Fatalf("a seeded Event reads %v, want excluded", got.Subject.Read.State)
	}

	// A seeded manager whose kind Radar doesn't collect has no list to read.
	managers := ownershiptest.Fixture()
	managers.Collection[ownership.ArgoApplication] = ownershiptest.Collection{State: ownershiptest.NotCollected, OnDemand: true}
	app := at("argoproj.io", "Application", "argocd", "web")
	seeded := view(t, managers, ownership.Unrestricted())
	seeded.Seed(ownership.Observation{Ref: app, UID: "uid-argocd-web"})
	if got := seeded.Members(app); got.Membership != ownership.MembersUnreadable || len(got.Gaps) != 1 || got.Gaps[0].Reason != readreason.RadarKindNotCollected {
		t.Fatalf("members of an uncollected manager = %v gaps %+v; want unreadable, as Radar doesn't collect it", got.Membership, got.Gaps)
	}

	// Seeding grants no access.
	denied := view(t, w, access{denied: []resourceid.GroupKind{configMaps}})
	denied.Seed(observation)
	if got := denied.Owner(fetched); got.Subject.Present() {
		t.Fatal("a seed let the caller read an object it may not read")
	}
}

// referenceIndex is where the chosen reference sits among the subject's own
// owner references, or -1 when none was chosen or it isn't one of them.
func referenceIndex(a ownership.OwnerAnswer) int {
	refs := a.Subject.Read.Object.OwnerReferences
	for i := range refs {
		if a.Reference == &refs[i] {
			return i
		}
	}
	return -1
}

func childRefs(a ownership.ChildrenAnswer) []string {
	var out []string
	for _, c := range a.Children {
		out = append(out, c.Ref.String())
	}
	return out
}

func TestViewChildrenResolveToTheOwnersCurrentIncarnation(t *testing.T) {
	w := ownershiptest.Fixture()
	unlistedWidget := []ownership.Gap{{Kind: resourceid.GroupKind{Group: "example.io", Kind: "Widget"}, Reason: readreason.KindUnknown}}
	for _, tc := range []struct {
		name   string
		access ownership.Access
		owner  resourceid.Ref
		want   []string
		narrow bool
		// gaps are the references to the owner Radar can't resolve.
		gaps []ownership.Gap
	}{
		// The stray Pod in beta names the ReplicaSet's UID but resolves in its
		// own namespace; wrong-coordinates names the UID under another kind.
		{"a ReplicaSet's Pods", ownership.Unrestricted(), at("apps", "ReplicaSet", "alpha", "web-1"),
			[]string{"Pod alpha/web-1-a", "Pod alpha/web-1-b"}, false, nil},
		{"a controlled and a plain child", ownership.Unrestricted(), at("apps", "Deployment", "alpha", "web"),
			[]string{"ReplicaSet.apps alpha/web-1", "Widget.example.io alpha/w1"}, false, nil},
		// s1's kind can't be watched; n2 is cluster-scoped, which a namespaced
		// owner can't own. j1 names a version discovery doesn't list, which the
		// collector retries, so j1 may yet be a child.
		{"through any served version", ownership.Unrestricted(), at("example.io", "Widget", "alpha", "w1"),
			[]string{"ConfigMap alpha/shared", "ConfigMap alpha/versioned"}, false, unlistedWidget},
		{"none for an earlier incarnation's dependent", ownership.Unrestricted(), at("apps", "ReplicaSet", "alpha", "web-old"), nil, false, nil},
		{"none from an Event", ownership.Unrestricted(), at("", "Pod", "alpha", "web-1-a"), nil, false, nil},
		{"a cluster-scoped owner's, anywhere", ownership.Unrestricted(), at("", "Node", "", "n1"),
			[]string{"ClusterWidget.example.io cw1", "Gadget.example.io beta/g1"}, false, nil},
		{"only those the caller may read", access{namespaces: []string{"alpha"}}, at("", "Node", "", "n1"),
			[]string{"ClusterWidget.example.io cw1"}, true, nil},
		{"none of a kind the caller may not read", access{denied: []resourceid.GroupKind{configMaps}}, at("example.io", "Widget", "alpha", "w1"), nil, true, unlistedWidget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := view(t, w, tc.access).Children(tc.owner)
			if !got.Subject.Present() || !slices.Equal(got.Gaps, tc.gaps) || got.Complete() != (len(tc.gaps) == 0) {
				t.Fatalf("Children = %+v; want gaps %v", got, tc.gaps)
			}
			if refs := childRefs(got); !slices.Equal(refs, tc.want) || got.Narrowed != tc.narrow {
				t.Fatalf("Children = %q narrowed=%v; want %q narrowed=%v", refs, got.Narrowed, tc.want, tc.narrow)
			}
		})
	}
}

func TestViewChildrenReportWhatTheSearchCouldNotRead(t *testing.T) {
	w := ownershiptest.Fixture()
	w.Collection[pods] = ownershiptest.Collection{State: ownershiptest.Loading}
	got := view(t, w, ownership.Unrestricted()).Children(at("apps", "ReplicaSet", "alpha", "web-1"))
	// One store serves every namespace, so its gap names none.
	want := []ownership.Gap{{Kind: pods, Reason: readreason.KindSyncPending}}
	if len(got.Children) != 0 || !slices.Equal(got.Gaps, want) || got.Complete() {
		t.Fatalf("Children while Pods load = %q gaps %+v; want none and %+v", childRefs(got), got.Gaps, want)
	}
	// A caller who may not read Pods hears nothing of Radar's Pod stores.
	got = view(t, w, access{denied: []resourceid.GroupKind{pods}}).Children(at("apps", "ReplicaSet", "alpha", "web-1"))
	if len(got.Gaps) != 0 || !got.Narrowed {
		t.Fatalf("Children for a caller denied Pods = gaps %+v narrowed=%v", got.Gaps, got.Narrowed)
	}
}

func TestViewControllerChain(t *testing.T) {
	w := ownershiptest.Fixture()
	stray := at("", "Pod", "beta", "stray")
	for _, tc := range []struct {
		name   string
		access ownership.Access
		start  resourceid.Ref
		end    resourceid.Ref
		stop   ownership.OwnerState
	}{
		{"through a ReplicaSet", ownership.Unrestricted(), at("", "Pod", "alpha", "web-1-a"), at("apps", "Deployment", "alpha", "web"), 0},
		{"an object with no controller", ownership.Unrestricted(), at("apps", "Deployment", "alpha", "web"), at("apps", "Deployment", "alpha", "web"), 0},
		{"stops at a controller that's gone", ownership.Unrestricted(), stray, stray, ownership.OwnerGone},
		{"stops at a withheld controller", access{denied: []resourceid.GroupKind{replicaSets}}, at("", "Pod", "alpha", "web-1-a"), at("", "Pod", "alpha", "web-1-a"), ownership.OwnerWithheld},
		{"ends at a Node, outside the chain", ownership.Unrestricted(), at("example.io", "Gadget", "beta", "g1"), at("example.io", "Gadget", "beta", "g1"), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := view(t, w, tc.access).ControllerChain(tc.start)
			stop := ownership.OwnerState(0)
			if got.UnfollowedReference != nil {
				stop = got.UnfollowedReference.State
			}
			if got.Chain[len(got.Chain)-1] != tc.end || (got.UnfollowedReference != nil) != (tc.stop != 0) || stop != tc.stop || got.Cycle || got.Truncated {
				t.Fatalf("ControllerChain = %+v (stop %v); want to end at %v, stop %v", got, stop, tc.end, tc.stop)
			}
		})
	}
}

func TestViewControllerChainTerminals(t *testing.T) {
	b := ownershiptest.NewBuilder()
	missingNode := metav1.OwnerReference{APIVersion: "v1", Kind: "Node", Name: "gone", UID: "uid-gone"}
	yes := true
	missingNode.Controller = &yes
	mirror := b.Add("", "Pod", "kube-system", "etcd-gone", missingNode)
	refTo := func(name string) metav1.OwnerReference {
		return metav1.OwnerReference{APIVersion: "example.io/v1", Kind: "Widget", Name: name, UID: types.UID("uid-x-" + name), Controller: &yes}
	}
	loopA := b.Add("example.io", "Widget", "x", "a", refTo("b"))
	loopB := b.Add("example.io", "Widget", "x", "b", refTo("a"))
	tail := b.Add("example.io", "Widget", "x", "tail", refTo("b"))
	const length = 48
	for i := range length {
		b.Add("example.io", "Widget", "chain", fmt.Sprint(i), metav1.OwnerReference{APIVersion: "example.io/v1", Kind: "Widget", Name: fmt.Sprint(i + 1), UID: types.UID(fmt.Sprintf("uid-chain-%d", i+1)), Controller: &yes})
	}
	v := view(t, b.World(), ownership.Unrestricted())

	if got := v.ControllerChain(mirror.Ref); !slices.Equal(got.Chain, []resourceid.Ref{mirror.Ref}) || got.UnfollowedReference == nil || got.UnfollowedReference.State != ownership.OwnerGone {
		t.Errorf("a mirror Pod of a deleted Node = %+v; want itself alone, stopped by the Node, which is gone", got)
	}
	for _, start := range []ownershiptest.Object{loopA, loopB, tail} {
		if got := v.ControllerChain(start.Ref); !got.Cycle || !slices.Contains(got.Chain, loopA.Ref) || !slices.Contains(got.Chain, loopB.Ref) || got.UnfollowedReference != nil {
			t.Errorf("from %v = %+v; want a cycle through a and b from any start", start.Ref, got)
		}
	}
	if got := v.ControllerChain(at("example.io", "Widget", "chain", "0")); !got.Truncated || got.Chain[len(got.Chain)-1] != at("example.io", "Widget", "chain", "16") || len(got.Chain) != 17 {
		t.Errorf("an over-long chain = %+v; want a walk bounded at 16 references", got)
	}
	if got := v.ControllerChain(tail.Ref); !slices.Equal(got.Chain, []resourceid.Ref{tail.Ref, loopB.Ref, loopA.Ref}) {
		t.Errorf("chain into a loop = %v; want tail, b, a", got.Chain)
	}
}

func TestViewControllerChainFollowsEachController(t *testing.T) {
	v := view(t, ownershiptest.Fixture(), ownership.Unrestricted())
	pod := at("", "Pod", "alpha", "web-1-a")
	want := []resourceid.Ref{pod, at("apps", "ReplicaSet", "alpha", "web-1"), at("apps", "Deployment", "alpha", "web")}
	if got := v.ControllerChain(pod); !slices.Equal(got.Chain, want) {
		t.Fatalf("chain = %v, want %v", got.Chain, want)
	}
	stray := at("", "Pod", "beta", "stray")
	if got := v.ControllerChain(stray); !slices.Equal(got.Chain, []resourceid.Ref{stray}) {
		t.Fatalf("chain stopped at the first reference = %v", got.Chain)
	}
}

func TestViewMembers(t *testing.T) {
	w := ownershiptest.Fixture()
	app := func(name string) resourceid.Ref { return at("argoproj.io", "Application", "argocd", name) }
	refs := func(a ownership.MembersAnswer) []resourceid.Ref {
		var out []resourceid.Ref
		for _, m := range a.Members {
			out = append(out, m.Ref)
		}
		return out
	}
	full := view(t, w, ownership.Unrestricted()).Members(app("web"))
	want := []resourceid.Ref{
		at("", "ConfigMap", "alpha", "cfg"), at("", "Node", "", "n1"), at("apps", "Deployment", "alpha", "web"),
		at("example.io", "Gadget", "beta", "g1"), at("example.io", "Node", "alpha", "n1"), at("example.io", "Widget", "alpha", "w1"),
	}
	unserved := ownership.Gap{Kind: resourceid.GroupKind{Group: "example.io", Kind: "Unserved"}, Namespace: "alpha", Reason: readreason.KindUnknown}
	if full.Membership != ownership.MembersRecorded || !slices.Equal(refs(full), want) || full.Missing != 1 || full.Withheld != 0 || full.Unknown != 1 || !slices.Equal(full.Gaps, []ownership.Gap{unserved}) {
		t.Fatalf("members = %v %v missing %d withheld %d unknown %d gaps %+v", full.Membership, refs(full), full.Missing, full.Withheld, full.Unknown, full.Gaps)
	}
	alpha := view(t, w, access{namespaces: []string{"alpha", "argocd"}}).Members(app("web"))
	if slices.Contains(refs(alpha), at("example.io", "Gadget", "beta", "g1")) || alpha.Withheld != 1 || len(alpha.Members) != len(want)-1 {
		t.Fatalf("members a caller in alpha sees = %v withheld %d", refs(alpha), alpha.Withheld)
	}
	unsure := view(t, w, access{undecided: []resourceid.GroupKind{configMaps}}).Members(app("web"))
	checkFailed := ownership.Gap{Kind: configMaps, Namespace: "alpha", Reason: readreason.UserPermissionCheckFailed}
	if slices.Contains(refs(unsure), at("", "ConfigMap", "alpha", "cfg")) || unsure.Unknown != 2 || unsure.Withheld != 0 || !slices.Equal(unsure.Gaps, []ownership.Gap{checkFailed, unserved}) {
		t.Fatalf("members when the caller's ConfigMap access couldn't be checked = %v unknown %d withheld %d gaps %+v", refs(unsure), unsure.Unknown, unsure.Withheld, unsure.Gaps)
	}
	for name, state := range map[string]ownership.MembershipState{"elsewhere": ownership.MembersRemote, "new": ownership.MembersUnreadable, "empty-list": ownership.MembersRecorded} {
		if got := view(t, w, ownership.Unrestricted()).Members(app(name)); got.Membership != state || len(got.Members) != 0 || got.Missing+got.Withheld+got.Unknown != 0 {
			t.Errorf("%s = %+v; want %v and no members", name, got, state)
		}
	}
}

// A reference carrying an owner's UID under other coordinates never resolves
// to the owner, since UIDs are unique: however the read of those coordinates
// goes, the object isn't a child and leaves the answer complete. A reference
// Radar can't map to a kind may still be the owner's, and stays a gap.
func TestViewChildrenIgnoreReferencesThatCantNameTheOwner(t *testing.T) {
	b := ownershiptest.NewBuilder()
	rs := b.Add("apps", "ReplicaSet", "alpha", "web")
	b.Add("", "Pod", "alpha", "web-a", rs.OwnerRef(true))
	odd := rs.OwnerRef(false)
	odd.APIVersion, odd.Kind = "v1", "Node"
	b.Add("", "Pod", "alpha", "odd", odd)
	elsewhere := rs.OwnerRef(false)
	elsewhere.APIVersion, elsewhere.Name = "apps/v9", "other"
	b.Add("", "Pod", "alpha", "elsewhere", elsewhere)
	w := b.World()
	w.Collection[resourceid.GroupKind{Kind: "Node"}] = ownershiptest.Collection{State: ownershiptest.Loading}
	if got := view(t, w, ownership.Unrestricted()).Children(rs.Ref); !got.Complete() || len(got.Children) != 1 {
		t.Fatalf("children %q gaps %+v; a Node named web, or a reference to another name, can't be to the ReplicaSet", childRefs(got), got.Gaps)
	}

	unmapped := rs.OwnerRef(false)
	unmapped.APIVersion = "apps/v9"
	b.Add("", "Pod", "alpha", "unmapped", unmapped)
	got := view(t, b.World(), ownership.Unrestricted()).Children(rs.Ref)
	if want := (ownership.Gap{Kind: rs.Ref.GroupKind(), Reason: readreason.KindUnknown}); !slices.Contains(got.Gaps, want) {
		t.Fatalf("gaps %+v; want %+v for a reference to web at a version discovery doesn't list", got.Gaps, want)
	}
}

// An object naming its owner twice is one child.
func TestViewChildrenListAnObjectNamingItsOwnerTwiceOnce(t *testing.T) {
	b := ownershiptest.NewBuilder()
	rs := b.Add("apps", "ReplicaSet", "alpha", "web")
	b.Add("", "Pod", "alpha", "dup", rs.OwnerRef(true), rs.OwnerRef(false))
	v := view(t, b.World(), ownership.Unrestricted())
	v.Owner(at("", "Pod", "alpha", "dup"))
	if got := v.Children(rs.Ref); len(got.Children) != 1 {
		t.Fatalf("children = %q; want dup once", childRefs(got))
	}
}

// A manager may list an Event; ownership doesn't read Events, so the answer
// leaves it out rather than counting a member it can't say anything about.
func TestViewMembersLeaveOutEvents(t *testing.T) {
	b := ownershiptest.NewBuilder()
	cm := b.Add("", "ConfigMap", "alpha", "cm")
	app := b.Manager(ownership.ArgoApplication, "argocd", "app", ownership.MembersRecorded, "alpha", at("", "Event", "alpha", "e"), cm.Ref)
	for name, a := range map[string]ownership.Access{"everything": ownership.Unrestricted(), "no Events": access{denied: []resourceid.GroupKind{{Kind: "Event"}}}} {
		got := view(t, b.World(), a).Members(app.Ref)
		if len(got.Members) != 1 || got.Missing+got.Withheld+got.Unknown != 0 || len(got.Gaps) != 0 {
			t.Fatalf("%s: members %d missing %d withheld %d unknown %d gaps %+v; want the ConfigMap alone", name, len(got.Members), got.Missing, got.Withheld, got.Unknown, got.Gaps)
		}
	}
}

func TestDecisionNames(t *testing.T) {
	for d, want := range map[ownership.Decision]string{ownership.Denied: "denied", ownership.Allowed: "allowed", ownership.Undecided: "undecided", ownership.Decision(99): "denied"} {
		if got := d.String(); got != want {
			t.Errorf("Decision(%d) = %q, want %q", d, got, want)
		}
	}
}

// Owner and Children are one relation read from both ends: an object is its
// owner's child exactly when its reference to that owner resolves.
func TestViewOwnersAndChildrenAgree(t *testing.T) {
	worlds := []ownershiptest.World{ownershiptest.Fixture()}
	for seed := range uint64(30) {
		worlds = append(worlds, ownershiptest.Generate(seed))
	}
	for i, w := range worlds {
		v := view(t, w, ownership.Unrestricted())
		for _, o := range w.Objects {
			subject, resolutions := v.References(o.Ref)
			if !subject.Present() {
				continue
			}
			for _, r := range resolutions {
				if r.State != ownership.OwnerFound {
					continue
				}
				children := v.Children(r.Ref)
				if !children.Complete() {
					continue
				}
				if !slices.ContainsFunc(children.Children, func(c ownership.Observation) bool { return c.Ref == o.Ref }) {
					t.Errorf("world %d: %v resolves its owner %v, which doesn't list it among %q", i, o.Ref, r.Ref, childRefs(children))
				}
			}
			for _, c := range v.Children(o.Ref).Children {
				_, theirs := v.References(c.Ref)
				if !slices.ContainsFunc(theirs, func(r ownership.OwnerResolution) bool { return r.State == ownership.OwnerFound && r.Ref == o.Ref }) {
					t.Errorf("world %d: %v lists %v, whose references don't resolve to it", i, o.Ref, c.Ref)
				}
			}
		}
	}
}

// A caller who may read less gets withheld or narrowed answers, never a
// different named one, and never a name it may not read.
func TestViewAnswersOnlyNarrowForACallerWhoMayReadLess(t *testing.T) {
	worlds := []ownershiptest.World{ownershiptest.Fixture()}
	for seed := range uint64(30) {
		worlds = append(worlds, ownershiptest.Generate(seed))
	}
	callers := []access{
		{namespaces: []string{"alpha"}},
		{namespaces: []string{"beta", "gamma"}},
		{namespaces: []string{}},
		{denied: []resourceid.GroupKind{replicaSets}},
		{denied: []resourceid.GroupKind{{Kind: "Node"}, {Group: "example.io", Kind: "Widget"}}},
		{namespaces: []string{"alpha"}, undecided: []resourceid.GroupKind{configMaps}},
	}
	for i, w := range worlds {
		full := view(t, w, ownership.Unrestricted())
		for _, caller := range callers {
			v := view(t, w, caller)
			for _, o := range w.Objects {
				checkNarrows(t, fmt.Sprintf("world %d, %+v, %v", i, caller, o.Ref), full, v, caller, o.Ref)
			}
		}
	}
}

func checkNarrows(t *testing.T, where string, full, v *ownership.View, caller access, ref resourceid.Ref) {
	t.Helper()
	readable := func(r resourceid.Ref) bool { return caller.CanRead(r.GroupKind(), r.Namespace) == ownership.Allowed }
	subject, got := v.References(ref)
	if !readable(ref) {
		if subject.Present() || got != nil {
			t.Errorf("%s: answers for an object the caller may not read", where)
		}
		return
	}
	_, want := full.References(ref)
	if len(got) != len(want) {
		t.Fatalf("%s: %d references, want %d", where, len(got), len(want))
	}
	for i := range got {
		switch {
		case got[i].State == want[i].State && got[i].Ref == want[i].Ref && got[i].Gap == want[i].Gap:
		case got[i].Ref == want[i].Ref && !readable(want[i].Ref) && (got[i].State == ownership.OwnerWithheld || got[i].Gap.Reason == readreason.UserPermissionCheckFailed):
		default:
			t.Errorf("%s: reference %d = %v %v %+v, full %v %v %+v", where, i, got[i].State, got[i].Ref, got[i].Gap, want[i].State, want[i].Ref, want[i].Gap)
		}
	}
	children, all := v.Children(ref), full.Children(ref)
	for _, c := range children.Children {
		if !readable(c.Ref) || !slices.ContainsFunc(all.Children, func(f ownership.Observation) bool { return f.Ref == c.Ref }) {
			t.Errorf("%s: child %v isn't one the full view has and the caller may read", where, c.Ref)
		}
	}
	if len(children.Children) < len(all.Children) && !children.Narrowed {
		t.Errorf("%s: %d of %d children without saying the answer is narrowed", where, len(children.Children), len(all.Children))
	}
	chain, fullChain := v.ControllerChain(ref), full.ControllerChain(ref)
	if !slices.Equal(chain.Chain, fullChain.Chain) && (chain.UnfollowedReference == nil || chain.UnfollowedReference.State != ownership.OwnerWithheld && chain.UnfollowedReference.Gap.Reason != readreason.UserPermissionCheckFailed) {
		t.Errorf("%s: chain %v, full %v, without a withheld stop", where, chain.Chain, fullChain.Chain)
	}
	if ownership.IsRecordingManager(ref.GroupKind()) {
		members, allMembers := v.Members(ref), full.Members(ref)
		for _, m := range members.Members {
			if !readable(m.Ref) || !slices.ContainsFunc(allMembers.Members, func(f ownership.Observation) bool { return f.Ref == m.Ref }) {
				t.Errorf("%s: member %v isn't one the full view has and the caller may read", where, m.Ref)
			}
		}
		if hidden := len(allMembers.Members) - len(members.Members); hidden > members.Withheld+members.Unknown {
			t.Errorf("%s: %d members dropped, %d withheld and %d unknown", where, hidden, members.Withheld, members.Unknown)
		}
	}
}

// managersWithGaps answers RecordingManagers with the session's answer plus gaps.
type managersWithGaps struct {
	ownership.ReadSession
	gaps []ownership.Gap
}

func (s managersWithGaps) RecordingManagers(ref resourceid.Ref) ownership.RecordingManagersRead {
	r := s.ReadSession.RecordingManagers(ref)
	r.Gaps = append(r.Gaps, s.gaps...)
	return r
}

// A caller learns which manager kinds Radar couldn't search, and where among
// the namespaces it may read, but nothing of the namespaces it can't.
func TestRecordingManagersKeepGapsTheCallerMayRead(t *testing.T) {
	s, err := ownershiptest.Scan(ownershiptest.Fixture()).Begin()
	if err != nil {
		t.Fatal(err)
	}
	everywhere := ownership.Gap{Kind: ownership.FluxHelmRelease, Reason: readreason.KindSyncPending}
	alpha := ownership.Gap{Kind: ownership.ArgoApplication, Namespace: "alpha", Reason: readreason.KindSyncPending}
	beta := ownership.Gap{Kind: ownership.ArgoApplication, Namespace: "beta", Reason: readreason.RadarForbidden}
	v := ownership.NewView(managersWithGaps{s, []ownership.Gap{everywhere, alpha, beta}}, access{namespaces: []string{"alpha"}})
	got := v.RecordingManagers(at("", "ConfigMap", "alpha", "cfg"))
	if !slices.Contains(got.Gaps, everywhere) || !slices.Contains(got.Gaps, alpha) || slices.Contains(got.Gaps, beta) {
		t.Fatalf("gaps = %v; want %v and %v, not %v", got.Gaps, everywhere, alpha, beta)
	}
}
