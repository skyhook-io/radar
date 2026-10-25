package ownership_test

import (
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// countingSession counts the reads a view makes, and can move every read
// after the first search to another world, as the cluster would change
// between two reads of one request.
type countingSession struct {
	now, later ownership.ReadSession
	switchAt   string
	objects    map[resourceid.Ref]int
	searches   map[types.UID]int
	validate   error
}

func newCountingSession(t *testing.T, w ownershiptest.World) *countingSession {
	t.Helper()
	s, err := ownershiptest.Scan(w).Begin()
	if err != nil {
		t.Fatal(err)
	}
	return &countingSession{now: s, objects: map[resourceid.Ref]int{}, searches: map[types.UID]int{}}
}

func (c *countingSession) after(method string) {
	if c.later != nil && c.switchAt == method {
		c.now, c.later = c.later, nil
	}
}

func (c *countingSession) Catalog() *ownership.Catalog { return c.now.Catalog() }
func (c *countingSession) Object(ref resourceid.Ref) ownership.ObjectRead {
	c.objects[ref]++
	defer c.after("Object")
	return c.now.Object(ref)
}
func (c *countingSession) DependentCandidates(uid types.UID, d ownership.SearchScope) ownership.DependentCandidatesRead {
	c.searches[uid]++
	defer c.after("DependentCandidates")
	return c.now.DependentCandidates(uid, d)
}
func (c *countingSession) Members(ref resourceid.Ref) ownership.MembersRead {
	return c.now.Members(ref)
}
func (c *countingSession) RecordingManagers(ref resourceid.Ref) ownership.RecordingManagersRead {
	return c.now.RecordingManagers(ref)
}
func (c *countingSession) ValidateCaches() error {
	if c.validate != nil {
		return c.validate
	}
	return c.now.ValidateCaches()
}

func TestViewReadsEachObjectAndSearchesEachOwnerOnce(t *testing.T) {
	s := newCountingSession(t, ownershiptest.Fixture())
	v := ownership.NewView(s, ownership.Unrestricted())
	rs := at("apps", "ReplicaSet", "alpha", "web-1")
	for range 3 {
		v.Owner(at("", "Pod", "alpha", "web-1-a"))
		v.Children(rs)
		v.ControllerChain(at("", "Pod", "alpha", "web-1-b"))
	}
	for ref, n := range s.objects {
		if n != 1 {
			t.Errorf("%v read %d times", ref, n)
		}
	}
	for uid, n := range s.searches {
		if n != 1 {
			t.Errorf("dependents of %s searched %d times", uid, n)
		}
	}
}

// An answer that found nothing is still an answer: an owner without
// children isn't searched again.
func TestViewReadsOnceWhenThereIsNothingToFind(t *testing.T) {
	b := ownershiptest.NewBuilder()
	rs := b.Add("apps", "ReplicaSet", "alpha", "empty")
	release := b.Add("helm.toolkit.fluxcd.io", "HelmRelease", "alpha", "legacy")
	s := newCountingSession(t, b.World())
	v := ownership.NewView(s, ownership.Unrestricted())
	for range 3 {
		if got := v.Children(rs.Ref); len(got.Children) != 0 {
			t.Fatalf("children of an empty ReplicaSet = %v", got.Children)
		}
	}
	if got := v.Members(release.Ref); got.Membership != ownership.MembersUnreadable {
		t.Fatalf("a HelmRelease without a recorded list is %v", got.Membership)
	}
	if n := s.searches[rs.UID]; n != 1 {
		t.Errorf("an owner without children searched %d times", n)
	}
}

// An object the search found is captured as found: the Owner answer asked
// after it reflects that capture, though the object changed since.
func TestViewAgreesWithItselfWhileTheClusterChanges(t *testing.T) {
	before := ownershiptest.Fixture()
	after := ownershiptest.Fixture()
	pod, rs := at("", "Pod", "alpha", "web-1-a"), at("apps", "ReplicaSet", "alpha", "web-1")
	for i := range after.Objects {
		if after.Objects[i].Ref == pod {
			// Orphaned: the same Pod, no longer naming its ReplicaSet.
			after.Objects[i].OwnerReferences = nil
		}
	}
	owned := func(v *ownership.View) bool {
		o := v.Owner(pod)
		return o.Resolution.State == ownership.OwnerFound && o.Resolution.Ref == rs
	}
	listed := func(v *ownership.View) bool {
		return slices.ContainsFunc(v.Children(rs).Children, func(c ownership.Observation) bool { return c.Ref == pod })
	}

	// Children first, then the Pod changes, then Owner.
	s := newCountingSession(t, before)
	later, _ := ownershiptest.Scan(after).Begin()
	s.later, s.switchAt = later, "DependentCandidates"
	v := ownership.NewView(s, ownership.Unrestricted())
	if !listed(v) || !owned(v) {
		t.Errorf("after the search found the Pod: listed=%v owned=%v; a view must agree with itself", listed(v), owned(v))
	}
}

// An object the search finds again after the view read it answers as read:
// here the Pod's reference stops being its controller in between.
func TestViewAnswersFromItsCaptureWhenTheSearchFindsANewerCopy(t *testing.T) {
	before := ownershiptest.Fixture()
	after := ownershiptest.Fixture()
	pod, rs := at("", "Pod", "alpha", "web-1-a"), at("apps", "ReplicaSet", "alpha", "web-1")
	for i := range after.Objects {
		if after.Objects[i].Ref == pod {
			r := after.Objects[i].OwnerReferences[0]
			r.Controller = nil
			after.Objects[i].OwnerReferences = []metav1.OwnerReference{r}
			after.Objects[i].ResourceVersion = "changed"
		}
	}
	s := newCountingSession(t, before)
	later, _ := ownershiptest.Scan(after).Begin()
	s.later, s.switchAt = later, "Object"
	v := ownership.NewView(s, ownership.Unrestricted())
	if o := v.Owner(pod); o.Resolution.State != ownership.OwnerFound || o.Reference == nil || !isControllerRef(*o.Reference) {
		t.Fatalf("scenario: the Pod's first read must name its ReplicaSet as controller")
	}
	i := slices.IndexFunc(v.Children(rs).Children, func(c ownership.Observation) bool { return c.Ref == pod })
	if i < 0 || !isControllerRef(v.Children(rs).Children[i].OwnerReferences[0]) {
		t.Fatalf("Children of the ReplicaSet must list the Pod as the controlled child the view read")
	}
}

func isControllerRef(r metav1.OwnerReference) bool { return r.Controller != nil && *r.Controller }

// A Pod the view read while Pods loaded is unknown to it. Once they have
// loaded, the ReplicaSet's search finds the Pod naming it; leaving the Pod out
// must not leave the children complete, since the view can't say whether it
// is one.
func TestViewChildrenSayWhatTheyReadBeforeItCouldBeRead(t *testing.T) {
	b := ownershiptest.NewBuilder()
	rs := b.Add("apps", "ReplicaSet", "alpha", "web")
	pod := b.Add("", "Pod", "alpha", "web-a", rs.OwnerRef(true))
	loading, loaded := b.World(), b.World()
	loading.Collection[pods] = ownershiptest.Collection{State: ownershiptest.Loading}
	s := newCountingSession(t, loading)
	later, err := ownershiptest.Scan(loaded).Begin()
	if err != nil {
		t.Fatal(err)
	}
	s.later, s.switchAt = later, "Object"
	v := ownership.NewView(s, ownership.Unrestricted())
	if got := v.Owner(pod.Ref); got.Subject.Read.State != ownership.Unavailable {
		t.Fatalf("scenario: the Pod must read unavailable while Pods load, got %v", got.Subject.Read.State)
	}
	got := v.Children(rs.Ref)
	want := []ownership.Gap{{Kind: pods, Namespace: "alpha", Reason: readreason.KindSyncPending}}
	if len(got.Children) != 0 || !slices.Equal(got.Gaps, want) || got.Complete() {
		t.Fatalf("Children after the Pods loaded = %q gaps %+v complete=%v; want none, and the gap of the Pod read before", childRefs(got), got.Gaps, got.Complete())
	}
}

// A gap on a namespaced kind across every namespace counts when the caller
// may read that kind in any of them, or its access couldn't be checked. When
// it may read the kind nowhere, naming the gap would say how Radar's
// collection of a kind the caller can't read is going; the gap is left out,
// and the answer is narrowed anyway.
func TestViewAuthorizesAnAllNamespacesGapByKind(t *testing.T) {
	w := ownershiptest.Fixture()
	w.Collection[pods] = ownershiptest.Collection{State: ownershiptest.Loading}
	node := at("", "Node", "", "n1")
	podGap := func(g ownership.Gap) bool { return g.Kind == pods && g.Reason == readreason.KindSyncPending }

	readsPods := view(t, w, access{denied: []resourceid.GroupKind{{Kind: "Secret"}}}).Children(node)
	if readsPods.Complete() || !slices.ContainsFunc(readsPods.Gaps, podGap) {
		t.Errorf("a caller who may read Pods everywhere: gaps %+v complete=%v; want the Pod gap", readsPods.Gaps, readsPods.Complete())
	}
	podsInAlpha := view(t, w, podsInAlphaOnly{}).Children(node)
	if podsInAlpha.Complete() || !slices.ContainsFunc(podsInAlpha.Gaps, podGap) {
		t.Errorf("a caller who may read Pods in one namespace: gaps %+v complete=%v; want the Pod gap", podsInAlpha.Gaps, podsInAlpha.Complete())
	}
	unsure := view(t, w, access{undecided: []resourceid.GroupKind{pods}}).Children(node)
	if !slices.ContainsFunc(unsure.Gaps, podGap) || !unsure.Narrowed {
		t.Errorf("a caller whose Pod access couldn't be checked: gaps %+v narrowed=%v; want the Pod gap, narrowed", unsure.Gaps, unsure.Narrowed)
	}
	noPods := view(t, w, access{denied: []resourceid.GroupKind{pods}}).Children(node)
	if slices.ContainsFunc(noPods.Gaps, func(g ownership.Gap) bool { return g.Kind == pods }) || !noPods.Narrowed {
		t.Errorf("a caller who may not read Pods: gaps %+v narrowed=%v; want no Pod gap, narrowed", noPods.Gaps, noPods.Narrowed)
	}
}

// podsInAlphaOnly may read every namespace, Pods only in alpha.
type podsInAlphaOnly struct{}

func (podsInAlphaOnly) CanRead(gk resourceid.GroupKind, namespace string) ownership.Decision {
	if gk == pods && namespace != "" && namespace != "alpha" {
		return ownership.Denied
	}
	return ownership.Allowed
}
func (podsInAlphaOnly) Namespaces() ([]string, bool) { return nil, true }
