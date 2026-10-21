package ownership_test

import (
	"errors"
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

// countingSession counts the reads a view makes, and can move every read
// after the first search to another world, as the cluster would change
// between two reads of one request.
type countingSession struct {
	now, later ownership.ReadSession
	switchAt   string
	objects    map[resourceid.Ref]int
	searches   map[types.UID]int
	inventory  map[resourceid.Ref]int
	validate   error
}

func newCountingSession(t *testing.T, w ownershiptest.World) *countingSession {
	t.Helper()
	s, err := ownershiptest.Scan(w).Begin()
	if err != nil {
		t.Fatal(err)
	}
	return &countingSession{now: s, objects: map[resourceid.Ref]int{}, searches: map[types.UID]int{}, inventory: map[resourceid.Ref]int{}}
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
func (c *countingSession) DependentCandidates(uid types.UID, d ownership.SearchScope, limit int) ownership.DependentCandidatesRead {
	c.searches[uid]++
	defer c.after("DependentCandidates")
	return c.now.DependentCandidates(uid, d, limit)
}
func (c *countingSession) Members(ref resourceid.Ref) ownership.MembersRead {
	c.inventory[ref]++
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

func TestViewValidateIsTheSessions(t *testing.T) {
	s := newCountingSession(t, ownershiptest.Fixture())
	s.validate = ownership.ErrCachesReplaced
	if err := ownership.NewView(s, ownership.Unrestricted()).ValidateAnswers(); !errors.Is(err, ownership.ErrCachesReplaced) {
		t.Fatalf("ValidateAnswers = %v, want the session's ErrCachesReplaced", err)
	}
}

func TestViewReadsEachObjectAndSearchesEachOwnerOnce(t *testing.T) {
	s := newCountingSession(t, ownershiptest.Fixture())
	v := ownership.NewView(s, ownership.Unrestricted())
	rs, app := at("apps", "ReplicaSet", "alpha", "web-1"), at("argoproj.io", "Application", "argocd", "web")
	for range 3 {
		v.Owner(at("", "Pod", "alpha", "web-1-a"))
		v.Children(rs, 0)
		v.Children(rs, 1)
		v.ControllerChain(at("", "Pod", "alpha", "web-1-b"))
		v.Members(app)
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
	if n := s.inventory[app]; n != 1 {
		t.Errorf("the Application's member list read %d times", n)
	}
}

// catalogCounter counts the view's catalog lookups: it makes one for each
// object it considers for an owner's children.
type catalogCounter struct {
	ownership.ReadSession
	lookups int
}

func (c *catalogCounter) Catalog() *ownership.Catalog {
	c.lookups++
	return c.ReadSession.Catalog()
}

// A walk over an owner's dependents, as a delete estimate makes, considers
// each object it captured for the owners the object names, not for every
// owner it asks about, so its work grows with the objects and not their
// square.
func TestViewConsidersEachCaptureForTheOwnersItNames(t *testing.T) {
	b := ownershiptest.NewBuilder()
	rs := b.Add("apps", "ReplicaSet", "alpha", "web")
	const n = 200
	var pods []resourceid.Ref
	for i := range n {
		pods = append(pods, b.Add("", "Pod", "alpha", fmt.Sprint("web-", i), rs.OwnerRef(true)).Ref)
	}
	s, err := ownershiptest.Scan(b.World()).Begin()
	if err != nil {
		t.Fatal(err)
	}
	c := &catalogCounter{ReadSession: s}
	v := ownership.NewView(c, ownership.Unrestricted())
	if got := v.Children(rs.Ref, 0); len(got.Children) != n {
		t.Fatalf("children of the ReplicaSet = %d, want %d", len(got.Children), n)
	}
	for _, p := range pods {
		v.Children(p, 0)
	}
	if c.lookups > 4*n {
		t.Errorf("a walk over %d children made %d catalog lookups; want a few per child", n, c.lookups)
	}
}

// An answer that found nothing is still an answer: an owner without
// children, or a manager whose member list can't be read, isn't asked again.
func TestViewReadsOnceWhenThereIsNothingToFind(t *testing.T) {
	b := ownershiptest.NewBuilder()
	rs := b.Add("apps", "ReplicaSet", "alpha", "empty")
	release := b.Add("helm.toolkit.fluxcd.io", "HelmRelease", "alpha", "legacy")
	s := newCountingSession(t, b.World())
	v := ownership.NewView(s, ownership.Unrestricted())
	for range 3 {
		if got := v.Children(rs.Ref, 0); len(got.Children) != 0 {
			t.Fatalf("children of an empty ReplicaSet = %v", got.Children)
		}
		if got := v.Members(release.Ref); got.Membership != ownership.MembersUnreadable {
			t.Fatalf("a HelmRelease without a recorded list is %v", got.Membership)
		}
	}
	if n := s.searches[rs.UID]; n != 1 {
		t.Errorf("an owner without children searched %d times", n)
	}
	if n := s.inventory[release.Ref]; n != 1 {
		t.Errorf("an unreadable member list read %d times", n)
	}
}

// Once a view has seen an object, every answer reflects that capture, though
// the object changes before the next read.
func TestViewAgreesWithItselfWhileTheClusterChanges(t *testing.T) {
	before := ownershiptest.Fixture()
	after := ownershiptest.Fixture()
	pod, rs := at("", "Pod", "alpha", "web-1-a"), at("apps", "ReplicaSet", "alpha", "web-1")
	for i := range after.Objects {
		if after.Objects[i].Ref == pod {
			// Orphaned: the same Pod, no longer naming its ReplicaSet.
			after.Objects[i].Owners = nil
		}
	}
	owned := func(v *ownership.View) bool {
		o := v.Owner(pod)
		return o.Resolution.State == ownership.OwnerFound && o.Resolution.Ref == rs
	}
	listed := func(v *ownership.View) bool {
		return slices.ContainsFunc(v.Children(rs, 0).Children, func(c ownership.Observation) bool { return c.Ref == pod })
	}

	// Children first, then the Pod changes, then Owner.
	s := newCountingSession(t, before)
	later, _ := ownershiptest.Scan(after).Begin()
	s.later, s.switchAt = later, "DependentCandidates"
	v := ownership.NewView(s, ownership.Unrestricted())
	if !listed(v) || !owned(v) {
		t.Errorf("after the search found the Pod: listed=%v owned=%v; a view must agree with itself", listed(v), owned(v))
	}

	// Owner first, then the Pod changes, then Children: the search no longer
	// finds it, but the view's capture still names the ReplicaSet.
	s = newCountingSession(t, before)
	later, _ = ownershiptest.Scan(after).Begin()
	s.later, s.switchAt = later, "Object"
	v = ownership.NewView(s, ownership.Unrestricted())
	if !owned(v) || !listed(v) {
		t.Errorf("after reading the Pod: owned=%v listed=%v; a view must agree with itself", owned(v), listed(v))
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
			r := after.Objects[i].Owners[0]
			r.Controller = nil
			after.Objects[i].Owners = []metav1.OwnerReference{r}
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
	i := slices.IndexFunc(v.Children(rs, 0).Children, func(c ownership.Observation) bool { return c.Ref == pod })
	if i < 0 || !isControllerRef(v.Children(rs, 0).Children[i].OwnerReferences[0]) {
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
	got := v.Children(rs.Ref, 0)
	want := []ownership.Gap{{Kind: pods, Namespace: "alpha", Reason: readreason.KindSyncPending}}
	if len(got.Children) != 0 || !slices.Equal(got.Gaps, want) || got.Complete() {
		t.Fatalf("Children after the Pods loaded = %q gaps %+v complete=%v; want none, and the gap of the Pod read before", childRefs(got), got.Gaps, got.Complete())
	}
}

// A manager whose status changed after the view read it answers no member
// list, rather than one from another version than its other answers.
func TestViewMembersRefuseAManagerThatChangedBetweenReads(t *testing.T) {
	before := ownershiptest.Fixture()
	after := ownershiptest.Fixture()
	app := at("argoproj.io", "Application", "argocd", "web")
	for i := range after.Objects {
		if after.Objects[i].Ref == app {
			after.Objects[i].ResourceVersion = "changed"
			after.Objects[i].Members = nil
		}
	}
	s := newCountingSession(t, before)
	later, _ := ownershiptest.Scan(after).Begin()
	s.later, s.switchAt = later, "Object"
	v := ownership.NewView(s, ownership.Unrestricted())
	got := v.Members(app)
	if len(got.Members) != 0 || len(got.Gaps) != 1 || got.Gaps[0].Reason != readreason.KindSyncPending {
		t.Fatalf("members of a manager that changed between reads = %d members, gaps %+v; want none and a sync-pending gap", len(got.Members), got.Gaps)
	}
	if err := v.ValidateAnswers(); !errors.Is(err, ownership.ErrObjectsChanged) {
		t.Errorf("ValidateAnswers = %v; want ErrObjectsChanged, so the caller reads again", err)
	}
}

// A child the caller may not read neither takes a place under the limit nor
// says it is there.
func TestViewChildrenLimitCountsOnlyWhatTheCallerMayRead(t *testing.T) {
	b := ownershiptest.NewBuilder()
	widget := b.Add("example.io", "Widget", "alpha", "w")
	yes := true
	ref := widget.OwnerRef(false)
	ref.Controller = &yes
	b.Add("", "Pod", "alpha", "readable", ref)
	b.Add("", "ConfigMap", "alpha", "a-hidden", widget.OwnerRef(false))
	got := view(t, b.World(), access{denied: []resourceid.GroupKind{configMaps}}).Children(widget.Ref, 1)
	if len(got.Children) != 1 || got.Children[0].Ref.Name != "readable" || got.Truncated {
		t.Fatalf("Children(w, 1) = %q truncated=%v; want the readable Pod, not cut short by a hidden ConfigMap", childRefs(got), got.Truncated)
	}
	full := view(t, b.World(), ownership.Unrestricted()).Children(widget.Ref, 1)
	if len(full.Children) != 1 || !full.Truncated {
		t.Fatalf("Children(w, 1) for a caller who reads both = %q truncated=%v", childRefs(full), full.Truncated)
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

	readsPods := view(t, w, access{denied: []resourceid.GroupKind{{Kind: "Secret"}}}).Children(node, 0)
	if readsPods.Complete() || !slices.ContainsFunc(readsPods.Gaps, podGap) {
		t.Errorf("a caller who may read Pods everywhere: gaps %+v complete=%v; want the Pod gap", readsPods.Gaps, readsPods.Complete())
	}
	podsInAlpha := view(t, w, podsInAlphaOnly{}).Children(node, 0)
	if podsInAlpha.Complete() || !slices.ContainsFunc(podsInAlpha.Gaps, podGap) {
		t.Errorf("a caller who may read Pods in one namespace: gaps %+v complete=%v; want the Pod gap", podsInAlpha.Gaps, podsInAlpha.Complete())
	}
	unsure := view(t, w, access{undecided: []resourceid.GroupKind{pods}}).Children(node, 0)
	if !slices.ContainsFunc(unsure.Gaps, podGap) || !unsure.Narrowed {
		t.Errorf("a caller whose Pod access couldn't be checked: gaps %+v narrowed=%v; want the Pod gap, narrowed", unsure.Gaps, unsure.Narrowed)
	}
	noPods := view(t, w, access{denied: []resourceid.GroupKind{pods}}).Children(node, 0)
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
func (podsInAlphaOnly) Namespaces() ([]string, bool)            { return nil, true }
func (podsInAlphaOnly) CanReadEverything() bool                 { return false }
func (podsInAlphaOnly) CanReadAllKindsIn(namespace string) bool { return namespace == "alpha" }

// On a cluster that doesn't change, nothing a view reads contradicts a
// search it made: every search first, then every read, for any caller.
func TestViewValidatePassesOnAStableCluster(t *testing.T) {
	worlds := []ownershiptest.World{ownershiptest.Fixture()}
	for seed := range uint64(30) {
		worlds = append(worlds, ownershiptest.Generate(seed))
	}
	for i, w := range worlds {
		for _, a := range []ownership.Access{ownership.Unrestricted(), access{namespaces: []string{"alpha"}}} {
			v := view(t, w, a)
			for _, o := range w.Objects {
				v.Children(o.Ref, 0)
			}
			for _, o := range w.Objects {
				v.Owner(o.Ref)
				v.References(o.Ref)
				v.ControllerChain(o.Ref)
				if ownership.IsRecordingManager(o.Ref.GroupKind()) {
					v.Members(o.Ref)
				}
			}
			if err := v.ValidateAnswers(); err != nil {
				t.Errorf("world %d, access %T: ValidateAnswers = %v on a cluster that didn't change", i, a, err)
			}
		}
	}
}

// A Pod adopted after its new owner's dependents were searched is one the
// search should have found: the view would list no children for the
// ReplicaSet yet name it as the Pod's owner, so ValidateAnswers refuses the answers.
// A search that couldn't read Pods made no claim about them.
func TestViewValidateRefusesAnObjectAdoptedAfterTheSearch(t *testing.T) {
	for _, podsLoading := range []bool{false, true} {
		b := ownershiptest.NewBuilder()
		rs := b.Add("apps", "ReplicaSet", "alpha", "web")
		pod := b.Add("", "Pod", "alpha", "web-a")
		before, after := b.World(), b.World()
		after.Objects = slices.Clone(after.Objects)
		for i := range after.Objects {
			if after.Objects[i].Ref == pod.Ref {
				after.Objects[i].Owners = []metav1.OwnerReference{rs.OwnerRef(true)}
			}
		}
		if podsLoading {
			before.Collection[pods] = ownershiptest.Collection{State: ownershiptest.Loading}
		}
		s := newCountingSession(t, before)
		later, err := ownershiptest.Scan(after).Begin()
		if err != nil {
			t.Fatal(err)
		}
		s.later, s.switchAt = later, "DependentCandidates"
		v := ownership.NewView(s, ownership.Unrestricted())
		if got := v.Children(rs.Ref, 0); len(got.Children) != 0 || got.Complete() == podsLoading {
			t.Fatalf("pods loading %v: children before the adoption = %v complete=%v", podsLoading, got.Children, got.Complete())
		}
		if got := v.Owner(pod.Ref); got.Resolution.State != ownership.OwnerFound {
			t.Fatalf("pods loading %v: the adopted Pod's owner = %v", podsLoading, got.Resolution.State)
		}
		err = v.ValidateAnswers()
		if want := !podsLoading; errors.Is(err, ownership.ErrObjectsChanged) != want {
			t.Errorf("pods loading %v: ValidateAnswers = %v; want ErrObjectsChanged %v", podsLoading, err, want)
		}
	}
}

// Radar collects Pods only in alpha, so a Node's search reads alpha and says
// the namespaces Radar doesn't collect went unread. A Pod in alpha naming the
// Node, first read or seeded after the search, is one the search should have
// found; a Pod seeded in a namespace Radar doesn't collect is not.
func TestViewValidateRefusesAnAdoptionInANamespaceTheSearchRead(t *testing.T) {
	for _, tc := range []struct {
		name      string
		namespace string
		seed      bool
		want      error
	}{
		{"read in alpha", "alpha", false, ownership.ErrObjectsChanged},
		{"seeded in alpha", "alpha", true, ownership.ErrObjectsChanged},
		{"seeded where Radar doesn't collect Pods", "beta", true, nil},
	} {
		b := ownershiptest.NewBuilder()
		node := b.Add("", "Node", "", "n")
		before := b.World()
		pod := b.Add("", "Pod", tc.namespace, "late", node.OwnerRef(true))
		after := b.World()
		for _, w := range []ownershiptest.World{before, after} {
			w.Collection[pods] = ownershiptest.Collection{Namespaces: []string{"alpha"}}
		}
		s := newCountingSession(t, before)
		if !tc.seed {
			later, err := ownershiptest.Scan(after).Begin()
			if err != nil {
				t.Fatal(err)
			}
			s.later, s.switchAt = later, "DependentCandidates"
		}
		v := ownership.NewView(s, ownership.Unrestricted())
		if got := v.Children(node.Ref, 0); len(got.Children) != 0 || got.Complete() {
			t.Fatalf("%s: the Node's children before the Pod = %q complete=%v", tc.name, childRefs(got), got.Complete())
		}
		if tc.seed {
			v.Seed(ownershiptest.Observe(pod))
		}
		if got := v.Owner(pod.Ref); got.Resolution.State != ownership.OwnerFound {
			t.Fatalf("%s: the Pod's owner = %v", tc.name, got.Resolution.State)
		}
		if err := v.ValidateAnswers(); !errors.Is(err, tc.want) {
			t.Errorf("%s: ValidateAnswers = %v, want %v", tc.name, err, tc.want)
		}
	}
}

// A Pod the view first read as absent, created with the ReplicaSet as its
// owner before the ReplicaSet's children were answered from that absence,
// contradicts that answer once a fetched copy is seeded.
func TestViewValidateRefusesASeedThatContradictsAnAnswer(t *testing.T) {
	b := ownershiptest.NewBuilder()
	rs := b.Add("apps", "ReplicaSet", "alpha", "web")
	pod := b.Add("", "Pod", "alpha", "web-a", rs.OwnerRef(true))
	after := b.World()
	before := b.World()
	before.Objects = slices.DeleteFunc(slices.Clone(before.Objects), func(o ownershiptest.Object) bool { return o.Ref == pod.Ref })
	s := newCountingSession(t, before)
	later, err := ownershiptest.Scan(after).Begin()
	if err != nil {
		t.Fatal(err)
	}
	s.later, s.switchAt = later, "Object"
	v := ownership.NewView(s, ownership.Unrestricted())
	if got := v.Owner(pod.Ref); got.Subject.Read.State != ownership.Absent {
		t.Fatalf("the Pod before it exists reads %v", got.Subject.Read.State)
	}
	if got := v.Children(rs.Ref, 0); len(got.Children) != 0 || !got.Complete() {
		t.Fatalf("children answered from the absence = %v complete=%v", got.Children, got.Complete())
	}
	v.Seed(ownershiptest.Observe(pod))
	if err := v.ValidateAnswers(); !errors.Is(err, ownership.ErrObjectsChanged) {
		t.Errorf("ValidateAnswers = %v; want ErrObjectsChanged", err)
	}
}

// A seeded copy may stand in for a read nothing has used yet; replacing one
// an answer already used, or contradicting an answer about its owner's
// children, makes ValidateAnswers fail.
func TestViewSeedsOnlyWhatNoAnswerUsed(t *testing.T) {
	b := ownershiptest.NewBuilder()
	rs := b.Add("apps", "ReplicaSet", "alpha", "web")
	pod := b.Add("", "Pod", "alpha", "web-a", rs.OwnerRef(true))
	cached := b.World()
	cached.Objects = slices.DeleteFunc(slices.Clone(cached.Objects), func(o ownershiptest.Object) bool { return o.Ref == pod.Ref })
	for name, tc := range map[string]struct {
		before func(*ownership.View)
		want   error
	}{
		"seeded first":                      {func(*ownership.View) {}, nil},
		"after an answer read it as absent": {func(v *ownership.View) { v.ControllerChain(pod.Ref) }, ownership.ErrObjectsChanged},
		"after its owner's children":        {func(v *ownership.View) { v.Children(rs.Ref, 0) }, ownership.ErrObjectsChanged},
	} {
		v := view(t, cached, ownership.Unrestricted())
		tc.before(v)
		v.Seed(ownershiptest.Observe(pod))
		if got := v.Owner(pod.Ref); got.Resolution.State != ownership.OwnerFound {
			t.Errorf("%s: the seeded Pod's owner = %v", name, got.Resolution.State)
		}
		if err := v.ValidateAnswers(); !errors.Is(err, tc.want) {
			t.Errorf("%s: ValidateAnswers = %v, want %v", name, err, tc.want)
		}
	}
}

// The same contradiction found by another owner's search: a Pod with a
// second owner, adopted by the first after the first's search found nothing,
// turns up in the second's search naming both.
func TestViewValidateRefusesAnObjectAnotherSearchFindsAdopted(t *testing.T) {
	b := ownershiptest.NewBuilder()
	first := b.Add("apps", "ReplicaSet", "alpha", "first")
	second := b.Add("apps", "ReplicaSet", "alpha", "second")
	pod := b.Add("", "Pod", "alpha", "shared", second.OwnerRef(false))
	before, after := b.World(), b.World()
	after.Objects = slices.Clone(after.Objects)
	for i := range after.Objects {
		if after.Objects[i].Ref == pod.Ref {
			after.Objects[i].Owners = []metav1.OwnerReference{first.OwnerRef(false), second.OwnerRef(false)}
		}
	}
	s := newCountingSession(t, before)
	later, err := ownershiptest.Scan(after).Begin()
	if err != nil {
		t.Fatal(err)
	}
	s.later, s.switchAt = later, "DependentCandidates"
	v := ownership.NewView(s, ownership.Unrestricted())
	if got := v.Children(first.Ref, 0); len(got.Children) != 0 {
		t.Fatalf("the first owner's children before the adoption = %v", got.Children)
	}
	if got := v.Children(second.Ref, 0); len(got.Children) != 1 {
		t.Fatalf("the second owner's children = %v; want the shared Pod", got.Children)
	}
	if err := v.ValidateAnswers(); !errors.Is(err, ownership.ErrObjectsChanged) {
		t.Errorf("ValidateAnswers = %v; want ErrObjectsChanged", err)
	}
}

// A reference the view couldn't resolve leaves no kind unsearched, so it
// excuses no object adopted after the owner's search: a Widget naming w at a
// version discovery doesn't list doesn't hide another Widget adopted later.
func TestViewAnUnresolvedReferenceExcusesNoAdoption(t *testing.T) {
	b := ownershiptest.NewBuilder()
	w := b.Add("example.io", "Widget", "alpha", "w")
	unserved := w.OwnerRef(false)
	unserved.APIVersion = "example.io/v9"
	b.Add("batch", "Job", "alpha", "j", unserved)
	late := b.Add("example.io", "Widget", "alpha", "late")
	after := b.World()
	after.Objects = slices.Clone(after.Objects)
	for i := range after.Objects {
		if after.Objects[i].Ref == late.Ref {
			after.Objects[i].Owners = []metav1.OwnerReference{w.OwnerRef(true)}
		}
	}
	s := newCountingSession(t, b.World())
	later, err := ownershiptest.Scan(after).Begin()
	if err != nil {
		t.Fatal(err)
	}
	s.later, s.switchAt = later, "DependentCandidates"
	v := ownership.NewView(s, ownership.Unrestricted())
	v.Children(w.Ref, 0)
	if o := v.Owner(late.Ref); o.Resolution.State != ownership.OwnerFound {
		t.Fatalf("scenario: late must name w, got %v", o.Resolution.State)
	}
	if err := v.ValidateAnswers(); !errors.Is(err, ownership.ErrObjectsChanged) {
		t.Fatalf("ValidateAnswers = %v; want ErrObjectsChanged", err)
	}
}

// phased serves each read from the session its phase names, moving on after
// each Object or DependentCandidates call.
type phased struct {
	phases []ownership.ReadSession
	i      int
}

func (p *phased) cur() ownership.ReadSession  { return p.phases[min(p.i, len(p.phases)-1)] }
func (p *phased) Catalog() *ownership.Catalog { return p.cur().Catalog() }
func (p *phased) ValidateCaches() error       { return nil }
func (p *phased) Members(r resourceid.Ref) ownership.MembersRead {
	return p.cur().Members(r)
}
func (p *phased) RecordingManagers(r resourceid.Ref) ownership.RecordingManagersRead {
	return p.cur().RecordingManagers(r)
}
func (p *phased) Object(r resourceid.Ref) ownership.ObjectRead {
	defer func() { p.i++ }()
	return p.cur().Object(r)
}
func (p *phased) DependentCandidates(u types.UID, d ownership.SearchScope, l int) ownership.DependentCandidatesRead {
	defer func() { p.i++ }()
	return p.cur().DependentCandidates(u, d, l)
}

// A Pod the view read while Pods loaded, which the owner's search then found,
// is a gap of that one object: it excuses no other Pod in the namespace the
// search read in full, adopted after the search.
func TestViewAnUnreadObjectExcusesNoOtherAdoption(t *testing.T) {
	b := ownershiptest.NewBuilder()
	rs := b.Add("apps", "ReplicaSet", "alpha", "web")
	p1 := b.Add("", "Pod", "alpha", "p1", rs.OwnerRef(true))
	q := b.Add("", "Pod", "alpha", "q")
	loading, loaded, adopted := b.World(), b.World(), b.World()
	loading.Collection[pods] = ownershiptest.Collection{State: ownershiptest.Loading}
	adopted.Objects = slices.Clone(adopted.Objects)
	for i := range adopted.Objects {
		if adopted.Objects[i].Ref == q.Ref {
			adopted.Objects[i].Owners = []metav1.OwnerReference{rs.OwnerRef(true)}
		}
	}
	var sessions []ownership.ReadSession
	// p1 is read while Pods load; the ReplicaSet and the search see them
	// loaded; q is read after its adoption.
	for _, w := range []ownershiptest.World{loading, loaded, loaded, adopted} {
		s, err := ownershiptest.Scan(w).Begin()
		if err != nil {
			t.Fatal(err)
		}
		sessions = append(sessions, s)
	}
	v := ownership.NewView(&phased{phases: sessions}, ownership.Unrestricted())
	if got := v.Owner(p1.Ref); got.Subject.Read.State != ownership.Unavailable {
		t.Fatalf("scenario: p1 must read unavailable, got %v", got.Subject.Read.State)
	}
	v.Children(rs.Ref, 0)
	if o := v.Owner(q.Ref); o.Resolution.State != ownership.OwnerFound || o.Resolution.Ref != rs.Ref {
		t.Fatalf("scenario: q must name the ReplicaSet, got %v %v", o.Resolution.State, o.Resolution.Ref)
	}
	if err := v.ValidateAnswers(); !errors.Is(err, ownership.ErrObjectsChanged) {
		t.Fatalf("ValidateAnswers = %v; q was adopted after a search that read alpha's Pods in full", err)
	}
}
