package topology

import (
	"fmt"
	"reflect"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func ownershipView(t *testing.T, w ownershiptest.World, a ownership.Access) *ownership.View {
	t.Helper()
	s, err := ownershiptest.Scan(w).Begin()
	if err != nil {
		t.Fatal(err)
	}
	return ownership.NewView(s, a)
}

// fetched is ref's object in w as a handler would have fetched it.
func fetched(t *testing.T, w ownershiptest.World, ref resourceid.Ref) ownership.Observation {
	t.Helper()
	for _, o := range w.Objects {
		if o.Ref == ref {
			return ownership.Observation{Ref: o.Ref, UID: o.UID, OwnerReferences: o.OwnerReferences}
		}
	}
	t.Fatalf("no %v in the fixture", ref)
	return ownership.Observation{}
}

func refsText(refs []ResourceRef) []string {
	var out []string
	for _, r := range refs {
		out = append(out, refIdentity(r).String())
	}
	return out
}

func idRef(group, kind, namespace, name string) resourceid.Ref {
	return resourceid.Ref{Group: group, Kind: kind, Namespace: namespace, Name: name}
}

func TestApplyOwnershipSortsManagesEdges(t *testing.T) {
	w := ownershiptest.Fixture()
	v := ownershipView(t, w, ownership.Unrestricted())
	deploy, rs := idRef("apps", "Deployment", "alpha", "web"), idRef("apps", "ReplicaSet", "alpha", "web-1")
	pod := idRef("", "Pod", "alpha", "web-1-a")
	widget := idRef("example.io", "Widget", "alpha", "w2")

	// A Pod's edges: from its ReplicaSet (ownership), from the Deployment
	// (a shortcut past the ReplicaSet) and from an object that manages it
	// without owning it.
	rel := &Relationships{manageSources: []ResourceRef{refOf(rs), refOf(deploy), refOf(widget)}}
	ApplyOwnership(rel, v, fetched(t, w, pod))
	if rel.Owner == nil || refIdentity(*rel.Owner) != rs || !slices.Equal(refsText(rel.Owners), []string{rs.String()}) {
		t.Errorf("owner %v owners %v; want the ReplicaSet", rel.Owner, refsText(rel.Owners))
	}
	if rel.Deployment == nil || refIdentity(*rel.Deployment) != deploy {
		t.Errorf("deployment = %v, want the ReplicaSet's controller", rel.Deployment)
	}
	if !slices.Equal(refsText(rel.Managers), []string{widget.String()}) {
		t.Errorf("managers = %v; want the Widget only, not the owner or the shortcut", refsText(rel.Managers))
	}

	// The Deployment's edges: to its ReplicaSet (a child), to a Pod (a
	// shortcut to a descendant) and to an object it manages.
	rel = &Relationships{manageTargets: []ResourceRef{refOf(rs), refOf(pod), refOf(widget)}}
	ApplyOwnership(rel, v, fetched(t, w, deploy))
	if got := refsText(rel.Children); !slices.Equal(got, []string{rs.String(), idRef("example.io", "Widget", "alpha", "w1").String()}) {
		t.Errorf("children = %v", got)
	}
	if !slices.Equal(refsText(rel.Manages), []string{widget.String()}) {
		t.Errorf("manages = %v; want the Widget only", refsText(rel.Manages))
	}
	if cov := rel.Ownership; cov == nil || !cov.Children.Complete || cov.PrimaryOwnerState != "" {
		t.Errorf("coverage = %+v", cov)
	}
}

// An edge between an object and an earlier incarnation of its owner is
// ownership gone stale, not management.
func TestApplyOwnershipKeepsStaleOwnershipOutOfManagement(t *testing.T) {
	w := ownershiptest.Fixture()
	v := ownershipView(t, w, ownership.Unrestricted())
	pod, old := idRef("", "Pod", "alpha", "replaced"), idRef("apps", "ReplicaSet", "alpha", "web-old")
	rel := &Relationships{manageSources: []ResourceRef{refOf(old)}}
	ApplyOwnership(rel, v, fetched(t, w, pod))
	if rel.Owner != nil || rel.Ownership.PrimaryOwnerState != "replaced" || len(rel.Managers) != 0 {
		t.Errorf("owner %v state %q managers %v; want no owner, a replaced reference and no manager", rel.Owner, rel.Ownership.PrimaryOwnerState, refsText(rel.Managers))
	}
	rel = &Relationships{manageTargets: []ResourceRef{refOf(pod)}}
	ApplyOwnership(rel, v, fetched(t, w, old))
	if len(rel.Children) != 0 || len(rel.Manages) != 0 {
		t.Errorf("children %v manages %v; the Pod names an earlier ReplicaSet", refsText(rel.Children), refsText(rel.Manages))
	}
}

// An edge between an object and the owner its reference names through a
// version discovery doesn't list is ownership the collector can't map yet,
// never management.
func TestApplyOwnershipKeepsAnUnmappedOwnerOutOfManagement(t *testing.T) {
	w := ownershiptest.Fixture()
	v := ownershipView(t, w, ownership.Unrestricted())
	job, widget := idRef("batch", "Job", "alpha", "j1"), idRef("example.io", "Widget", "alpha", "w1")
	rel := &Relationships{manageSources: []ResourceRef{refOf(widget)}}
	ApplyOwnership(rel, v, fetched(t, w, job))
	if rel.Owner != nil || rel.Ownership.PrimaryOwnerState != "unknown" || rel.Ownership.OwnerReason != string(readreason.KindUnknown) || len(rel.Managers) != 0 {
		t.Errorf("owner %v state %q (%s) managers %v; want an unknown reference (kind_unknown) and no manager", rel.Owner, rel.Ownership.PrimaryOwnerState, rel.Ownership.OwnerReason, refsText(rel.Managers))
	}
	rel = &Relationships{manageTargets: []ResourceRef{refOf(job)}}
	ApplyOwnership(rel, v, fetched(t, w, widget))
	if slices.Contains(refsText(rel.Manages), job.String()) {
		t.Errorf("the Widget manages %v; the Job's reference names it", refsText(rel.Manages))
	}
}

// While a hop of the controller chain can't be read, an edge from beyond it
// may be a shortcut past it, so only a GitOps manager's edge counts as
// management.
func TestApplyOwnershipNeedsTheWholeChainToCallAnEdgeManagement(t *testing.T) {
	w := ownershiptest.Fixture()
	w.Collection[resourceid.GroupKind{Group: "apps", Kind: "ReplicaSet"}] = ownershiptest.Collection{State: ownershiptest.Loading}
	v := ownershipView(t, w, ownership.Unrestricted())
	pod := idRef("", "Pod", "alpha", "web-1-a")
	// An Application whose list can't be read, matched by the Pod's labels.
	deploy, app := idRef("apps", "Deployment", "alpha", "web"), idRef("argoproj.io", "Application", "argocd", "malformed")
	rel := &Relationships{manageSources: []ResourceRef{refOf(deploy), refOf(app)}}
	ApplyOwnership(rel, v, fetched(t, w, pod))
	if got := refsText(rel.Managers); !slices.Equal(got, []string{app.String()}) {
		t.Errorf("managers while ReplicaSets load = %v; want only the Application", got)
	}
	if rel.Ownership.PrimaryOwnerState != "unknown" || rel.Ownership.OwnerRef == nil || refIdentity(*rel.Ownership.OwnerRef) != idRef("apps", "ReplicaSet", "alpha", "web-1") {
		t.Errorf("owner state %q ref %v; want the unknown ReplicaSet the Pod names", rel.Ownership.PrimaryOwnerState, rel.Ownership.OwnerRef)
	}
	if rel.Ownership.OwnerReason != string(readreason.KindSyncPending) {
		t.Errorf("owner reason %q; want %q", rel.Ownership.OwnerReason, readreason.KindSyncPending)
	}
}

// A shortcut the graph drew through a hop of the controller chain stays a
// shortcut however the hop now reads: still loading, deleted, recreated, or
// named at a version the API no longer serves. Nothing past a broken hop is
// known, so neither end takes the edge for management.
func TestApplyOwnershipKeepsShortcutsThroughABrokenHopOutOfManagement(t *testing.T) {
	for _, hop := range []string{"loading", "gone", "replaced", "unserved version"} {
		t.Run(hop, func(t *testing.T) {
			b := ownershiptest.NewBuilder()
			deploy := b.Add("apps", "Deployment", "alpha", "web")
			rs := b.Add("apps", "ReplicaSet", "alpha", "web-1", deploy.OwnerRef(true))
			owner := rs.OwnerRef(true)
			if hop == "unserved version" {
				owner.APIVersion = "apps/v1beta1"
			}
			pod := b.Add("", "Pod", "alpha", "web-1-a", owner)
			w := b.World()
			w.Objects = slices.Clone(w.Objects)
			switch hop {
			case "loading":
				w.Collection[rs.Ref.GroupKind()] = ownershiptest.Collection{State: ownershiptest.Loading}
			case "gone":
				w.Objects = slices.DeleteFunc(w.Objects, func(o ownershiptest.Object) bool { return o.Ref == rs.Ref })
			case "replaced":
				for i := range w.Objects {
					if w.Objects[i].Ref == rs.Ref {
						w.Objects[i].UID = "web-1-recreated"
					}
				}
			}
			podRel := &Relationships{manageSources: []ResourceRef{refOf(deploy.Ref)}}
			ApplyOwnership(podRel, ownershipView(t, w, ownership.Unrestricted()), fetched(t, w, pod.Ref))
			deployRel := &Relationships{manageTargets: []ResourceRef{refOf(pod.Ref)}}
			ApplyOwnership(deployRel, ownershipView(t, w, ownership.Unrestricted()), fetched(t, w, deploy.Ref))
			if len(podRel.Managers) != 0 || len(deployRel.Manages) != 0 {
				t.Errorf("Pod managers %v, Deployment manages %v; want the shortcut in neither", refsText(podRel.Managers), refsText(deployRel.Manages))
			}
		})
	}
}

// A controller chain that loops or runs past the walk's bound never reaches
// an object without a controller, so an edge into it isn't evidence of
// management either.
func TestApplyOwnershipNeedsAChainThatEnds(t *testing.T) {
	for _, shape := range []string{"loop", "too long"} {
		t.Run(shape, func(t *testing.T) {
			b := ownershiptest.NewBuilder()
			manager := b.Add("example.io", "Gadget", "alpha", "manager")
			top := b.Add("example.io", "Widget", "alpha", "top")
			below := top
			for i := range 20 {
				below = b.Add("example.io", "Widget", "alpha", fmt.Sprintf("w-%02d", i), below.OwnerRef(true))
				if shape == "loop" {
					break
				}
			}
			w := b.World()
			w.Objects = slices.Clone(w.Objects)
			if shape == "loop" {
				for i := range w.Objects {
					if w.Objects[i].Ref == top.Ref {
						w.Objects[i].OwnerReferences = []metav1.OwnerReference{below.OwnerRef(true)}
					}
				}
			}
			rel := &Relationships{manageSources: []ResourceRef{refOf(manager.Ref)}}
			ApplyOwnership(rel, ownershipView(t, w, ownership.Unrestricted()), fetched(t, w, below.Ref))
			if len(rel.Managers) != 0 {
				t.Errorf("managers %v; want none for a chain that doesn't end", refsText(rel.Managers))
			}
		})
	}
}

// A GitOps manager whose member list can't be read lists the members the
// graph matched by their labels, as their own drawers name it, whatever
// their controllers are.
func TestApplyOwnershipTakesGitOpsLabelMembersWhateverTheirControllers(t *testing.T) {
	b := ownershiptest.NewBuilder()
	operator := b.Add("example.io", "Widget", "alpha", "operator")
	deploy := b.Add("apps", "Deployment", "alpha", "web", operator.OwnerRef(true))
	release := b.Add("helm.toolkit.fluxcd.io", "HelmRelease", "alpha", "release")
	w := b.World()
	w.Collection[operator.Ref.GroupKind()] = ownershiptest.Collection{State: ownershiptest.Loading}

	rel := &Relationships{manageTargets: []ResourceRef{refOf(deploy.Ref)}}
	ApplyOwnership(rel, ownershipView(t, w, ownership.Unrestricted()), fetched(t, w, release.Ref))
	if rel.Ownership.Manages == nil || rel.Ownership.Manages.Membership != "unreadable" || !slices.Equal(refsText(rel.Manages), []string{deploy.Ref.String()}) {
		t.Errorf("HelmRelease manages %v (%+v); want the Deployment its labels match", refsText(rel.Manages), rel.Ownership.Manages)
	}
	back := &Relationships{manageSources: []ResourceRef{refOf(release.Ref)}}
	ApplyOwnership(back, ownershipView(t, w, ownership.Unrestricted()), fetched(t, w, deploy.Ref))
	if !slices.Equal(refsText(back.Managers), []string{release.Ref.String()}) {
		t.Errorf("Deployment managers %v; want the HelmRelease", refsText(back.Managers))
	}
}

// An owner reference Radar couldn't resolve, because discovery didn't answer
// for its group, is named as the object names it, without a namespace Radar
// can't know.
func TestApplyOwnershipNamesAnOwnerItCouldNotResolve(t *testing.T) {
	b := ownershiptest.NewBuilder()
	widget := b.Add("example.io", "Widget", "alpha", "parent")
	pod := b.Add("", "Pod", "alpha", "p", widget.OwnerRef(true))
	w := b.World()
	w.Partial = true
	w.Resources = slices.DeleteFunc(slices.Clone(w.Resources), func(r ownership.ServedResource) bool { return r.Group == "example.io" })
	rel := &Relationships{}
	ApplyOwnership(rel, ownershipView(t, w, ownership.Unrestricted()), fetched(t, w, pod.Ref))
	want := ResourceRef{Group: "example.io", Kind: "Widget", Name: "parent"}
	if rel.Ownership.PrimaryOwnerState != "unknown" || rel.Ownership.OwnerRef == nil || *rel.Ownership.OwnerRef != want || rel.Ownership.OwnerReason == "" {
		t.Errorf("ownership = %+v ref %+v; want unknown, %+v, with a reason", rel.Ownership, rel.Ownership.OwnerRef, want)
	}
}

// A GitOps manager whose member list couldn't be read says why, apart from
// one whose status records no list.
func TestApplyOwnershipSaysWhyAMemberListCouldNotBeRead(t *testing.T) {
	w := ownershiptest.Fixture()
	app := idRef("argoproj.io", "Application", "argocd", "web")
	w.Collection[app.GroupKind()] = ownershiptest.Collection{State: ownershiptest.NotCollected}
	rel := &Relationships{}
	ApplyOwnership(rel, ownershipView(t, w, ownership.Unrestricted()), fetched(t, w, app))
	if m := rel.Ownership.Manages; m == nil || m.Membership != "unreadable" || m.UnavailableReason != string(readreason.RadarKindNotCollected) {
		t.Errorf("manages coverage = %+v; want unreadable for %s", m, readreason.RadarKindNotCollected)
	}
}

func TestApplyOwnershipForGitOpsManagers(t *testing.T) {
	w := ownershiptest.Fixture()
	v := ownershipView(t, w, ownership.Unrestricted())
	app := func(name string) resourceid.Ref { return idRef("argoproj.io", "Application", "argocd", name) }
	stray := ResourceRef{Kind: "ConfigMap", Namespace: "alpha", Name: "versioned"}

	rel := &Relationships{manageTargets: []ResourceRef{stray}}
	ApplyOwnership(rel, v, fetched(t, w, app("web")))
	if len(rel.Manages) != 6 || slices.Contains(refsText(rel.Manages), refIdentity(stray).String()) {
		t.Errorf("recorded manager manages %v; want its six held members and no edge", refsText(rel.Manages))
	}
	if m := rel.Ownership.Manages; m == nil || m.Membership != "recorded" || m.Missing != 1 || m.Unknown != 1 {
		t.Errorf("members coverage = %+v", m)
	}

	rel = &Relationships{manageTargets: []ResourceRef{stray}}
	ApplyOwnership(rel, v, fetched(t, w, app("elsewhere")))
	if len(rel.Manages) != 0 || rel.Ownership.Manages.Membership != "remote" {
		t.Errorf("remote manager manages %v (%+v); its list names another cluster's objects", refsText(rel.Manages), rel.Ownership.Manages)
	}

	rel = &Relationships{manageTargets: []ResourceRef{stray}}
	ApplyOwnership(rel, v, fetched(t, w, app("new")))
	if !slices.Equal(refsText(rel.Manages), []string{refIdentity(stray).String()}) || rel.Ownership.Manages.Membership != "unreadable" {
		t.Errorf("manager with no readable list manages %v (%+v); want the label-inferred edge", refsText(rel.Manages), rel.Ownership.Manages)
	}
}

func TestApplyOwnershipWithoutAnAnswer(t *testing.T) {
	w := ownershiptest.Fixture()
	pod := idRef("", "Pod", "alpha", "web-1-a")
	recreated := fetched(t, w, pod)
	recreated.UID = "uid-of-another-incarnation"
	for name, tc := range map[string]struct {
		access  ownership.Access
		fetched ownership.Observation
		check   func(*OwnershipCoverage) bool
	}{
		"the caller may not read it": {denyPods{}, fetched(t, w, pod), func(c *OwnershipCoverage) bool { return c.UnavailableReason == string(readreason.UserForbidden) }},
		"it was recreated":           {ownership.Unrestricted(), recreated, func(c *OwnershipCoverage) bool { return c.UnavailableReason == string(readreason.ReadError) }},
	} {
		t.Run(name, func(t *testing.T) {
			rel := &Relationships{Owner: &ResourceRef{Kind: "Deployment"}, Children: []ResourceRef{{Kind: "Pod"}}, manageSources: []ResourceRef{{Kind: "Application"}}}
			ApplyOwnership(rel, ownershipView(t, w, tc.access), tc.fetched)
			if rel.Owner != nil || rel.Children != nil || rel.Managers != nil || rel.Ownership == nil || !tc.check(rel.Ownership) {
				t.Fatalf("relationships = %+v coverage %+v", rel, rel.Ownership)
			}
		})
	}
}

// Radar fetches objects of kinds it doesn't collect, such as EndpointSlices,
// straight from the API; the fetched object answers for itself.
func TestApplyOwnershipForAKindRadarDoesNotCollect(t *testing.T) {
	w := ownershiptest.Fixture()
	w.Collection[resourceid.GroupKind{Kind: "ConfigMap"}] = ownershiptest.Collection{State: ownershiptest.NotCollected}
	cm := idRef("", "ConfigMap", "alpha", "versioned")
	rel := &Relationships{}
	ApplyOwnership(rel, ownershipView(t, w, ownership.Unrestricted()), fetched(t, w, cm))
	if rel.Owner == nil || refIdentity(*rel.Owner) != idRef("example.io", "Widget", "alpha", "w1") || rel.Ownership.UnavailableReason != "" {
		t.Fatalf("owner %v coverage %+v; want the Widget its reference names", rel.Owner, rel.Ownership)
	}
}

type denyPods struct{}

func (denyPods) CanRead(gk resourceid.GroupKind, _ string) ownership.Decision {
	if gk.Kind == "Pod" && gk.Group == "" {
		return ownership.Denied
	}
	return ownership.Allowed
}
func (denyPods) Namespaces() ([]string, bool) { return nil, true }

func TestApplyOwnershipCapsChildren(t *testing.T) {
	b := ownershiptest.NewBuilder()
	rs := b.Add("apps", "ReplicaSet", "alpha", "wide")
	for i := range maxOwnershipRefs + 20 {
		b.Add("", "Pod", "alpha", fmt.Sprintf("wide-%04d", i), rs.OwnerRef(true))
	}
	rel := &Relationships{}
	ApplyOwnership(rel, ownershipView(t, b.World(), ownership.Unrestricted()), ownership.Observation{Ref: rs.Ref, UID: rs.UID})
	if len(rel.Children) != maxOwnershipRefs || !rel.Ownership.Children.Truncated || rel.Ownership.Children.Complete {
		t.Fatalf("%d children, coverage %+v; want the first %d, marked truncated", len(rel.Children), rel.Ownership.Children, maxOwnershipRefs)
	}
}

// searchCounter is a session that counts the reads that grow with what an
// object owns or manages: searches for dependents and member lists.
type searchCounter struct {
	ownership.ReadSession
	searches int
}

func (s *searchCounter) DependentCandidates(uid types.UID, domain ownership.SearchScope) ownership.DependentCandidatesRead {
	s.searches++
	return s.ReadSession.DependentCandidates(uid, domain)
}

func (s *searchCounter) Members(manager resourceid.Ref) ownership.MembersRead {
	s.searches++
	return s.ReadSession.Members(manager)
}

func (s *searchCounter) RecordingManagers(ref resourceid.Ref) ownership.RecordingManagersRead {
	s.searches++
	return s.ReadSession.RecordingManagers(ref)
}

// Ownership doesn't read Events, so an Event's Related Resources carry no
// ownership answer at all rather than one with nothing in it and no reason.
func TestApplyOwnershipSaysNothingOfAnEvent(t *testing.T) {
	w := ownershiptest.Fixture()
	event := ownership.Observation{Ref: resourceid.Ref{Kind: "Event", Namespace: "alpha", Name: "e1"}, UID: "uid-alpha-e1"}
	rel := &Relationships{Owner: &ResourceRef{Kind: "Pod", Namespace: "alpha", Name: "stale"}}
	ApplyOwnership(rel, ownershipView(t, w, ownership.Unrestricted()), event)
	if rel.Ownership != nil || rel.Owner != nil || rel.Children != nil {
		t.Fatalf("an Event's ownership = %+v, owner %+v, children %v; want none", rel.Ownership, rel.Owner, rel.Children)
	}
}

// ApplyOwner gives every object the owner ApplyOwnership gives it, and why
// there is none, without searching for children or reading a member list.
func TestApplyOwnerAnswersTheOwnerWithoutSearching(t *testing.T) {
	w := ownershiptest.Fixture()
	for _, o := range w.Objects {
		full := &Relationships{}
		ApplyOwnership(full, ownershipView(t, w, ownership.Unrestricted()), fetched(t, w, o.Ref))
		s, err := ownershiptest.Scan(w).Begin()
		if err != nil {
			t.Fatal(err)
		}
		counted := &searchCounter{ReadSession: s}
		owner := &Relationships{}
		ApplyOwner(owner, ownership.NewView(counted, ownership.Unrestricted()), fetched(t, w, o.Ref))
		if counted.searches != 0 {
			t.Errorf("%v: the owner took %d searches for dependents or members", o.Ref, counted.searches)
		}
		if !reflect.DeepEqual(owner.Owner, full.Owner) {
			t.Errorf("%v: owner %v, want %v", o.Ref, owner.Owner, full.Owner)
		}
		got, want := owner.Ownership, full.Ownership
		if (got == nil) != (want == nil) {
			t.Errorf("%v: owner coverage %+v, want %+v", o.Ref, got, want)
			continue
		}
		if got != nil && (got.PrimaryOwnerState != want.PrimaryOwnerState || !reflect.DeepEqual(got.OwnerRef, want.OwnerRef) || got.OwnerReason != want.OwnerReason ||
			got.UnavailableReason != want.UnavailableReason) {
			t.Errorf("%v: owner coverage %+v, want %+v", o.Ref, got, want)
		}
	}
}

// A manager is named only when the caller may read it and it still exists:
// the graph's edge may come from a manager the caller can't see, or one
// deleted since the graph was built.
func TestApplyOwnershipNamesOnlyManagersTheCallerReads(t *testing.T) {
	b := ownershiptest.NewBuilder()
	cm := b.Add("", "ConfigMap", "alpha", "c")
	app := b.Manager(ownership.ArgoApplication, "argocd", "app", ownership.MembersUnreadable, "alpha")
	w := b.World()
	for name, tc := range map[string]struct {
		w      ownershiptest.World
		access ownership.Access
		want   int
	}{
		"readable":           {w, ownership.Unrestricted(), 1},
		"withheld":           {w, denyKind{ownership.ArgoApplication}, 0},
		"deleted since then": {ownershiptest.World{Resources: w.Resources, Objects: []ownershiptest.Object{cm}, Collection: w.Collection}, ownership.Unrestricted(), 0},
	} {
		rel := &Relationships{manageSources: []ResourceRef{refOf(app.Ref)}}
		v := ownershipView(t, tc.w, tc.access)
		ApplyOwnership(rel, v, fetched(t, tc.w, cm.Ref))
		if len(rel.Managers) != tc.want {
			t.Errorf("%s: managers %v, want %d", name, refsText(rel.Managers), tc.want)
		}
	}
}

// Managed By says when the search couldn't settle every manager the caller
// may read: an unreadable list may name the resource, and a remote list names
// its coordinates in a cluster Radar can't confirm is this one. A manager the
// caller may not read isn't counted, and a settled search says nothing.
func TestApplyOwnershipSaysWhichManagersItCouldNotSettle(t *testing.T) {
	b := ownershiptest.NewBuilder()
	cm := b.Add("", "ConfigMap", "alpha", "c")
	b.Manager(ownership.ArgoApplication, "argocd", "broken", ownership.MembersUnreadable, "alpha")
	b.Manager(ownership.FluxKustomization, "flux-system", "elsewhere", ownership.MembersRemote, "", cm.Ref)
	w := b.World()
	for name, tc := range map[string]struct {
		access ownership.Access
		want   *ManagersCoverage
	}{
		"readable":          {ownership.Unrestricted(), &ManagersCoverage{Unreadable: 1, Remote: 1}},
		"no Applications":   {denyKind{ownership.ArgoApplication}, &ManagersCoverage{Remote: 1}},
		"no Kustomizations": {denyKind{ownership.FluxKustomization}, &ManagersCoverage{Unreadable: 1}},
	} {
		rel := &Relationships{}
		ApplyOwnership(rel, ownershipView(t, w, tc.access), fetched(t, w, cm.Ref))
		if got := rel.Ownership.Managers; got == nil || !reflect.DeepEqual(*got, *tc.want) {
			t.Errorf("%s: ManagedBy %+v, want %+v", name, got, tc.want)
		}
	}
	settled := ownershiptest.NewBuilder()
	c := settled.Add("", "ConfigMap", "alpha", "c")
	settled.Manager(ownership.ArgoApplication, "argocd", "app", ownership.MembersRecorded, "alpha", c.Ref)
	sw := settled.World()
	rel := &Relationships{}
	ApplyOwnership(rel, ownershipView(t, sw, ownership.Unrestricted()), fetched(t, sw, c.Ref))
	if rel.Ownership.Managers != nil || len(rel.Managers) != 1 {
		t.Errorf("settled: coverage %+v, managers %v; want none and the one recorder", rel.Ownership.Managers, refsText(rel.Managers))
	}
}

type denyKind struct{ kind resourceid.GroupKind }

func (d denyKind) CanRead(gk resourceid.GroupKind, _ string) ownership.Decision {
	if gk == d.kind {
		return ownership.Denied
	}
	return ownership.Allowed
}
func (denyKind) Namespaces() ([]string, bool) { return nil, true }

// A Pod's Deployment is the one its ReplicaSet's controller reference names,
// reached through the Pod's own controller reference.
func TestApplyOwnershipNamesTheDeploymentOnlyThroughControllers(t *testing.T) {
	for name, tc := range map[string]struct {
		podControlled, replicaSetControlled bool
		want                                bool
	}{
		"both controllers":                            {true, true, true},
		"a Pod the ReplicaSet doesn't control":        {false, true, false},
		"a ReplicaSet the Deployment doesn't control": {true, false, false},
	} {
		b := ownershiptest.NewBuilder()
		d := b.Add("apps", "Deployment", "alpha", "d")
		rs := b.Add("apps", "ReplicaSet", "alpha", "d-1", d.OwnerRef(tc.replicaSetControlled))
		pod := b.Add("", "Pod", "alpha", "d-1-a", rs.OwnerRef(tc.podControlled))
		w := b.World()
		rel := &Relationships{}
		ApplyOwnership(rel, ownershipView(t, w, ownership.Unrestricted()), fetched(t, w, pod.Ref))
		if got := rel.Deployment != nil; got != tc.want {
			t.Errorf("%s: deployment %v", name, rel.Deployment)
		}
		if rel.Owner == nil || refIdentity(*rel.Owner) != rs.Ref {
			t.Errorf("%s: owner %v, want the ReplicaSet its only reference names", name, rel.Owner)
		}
	}
}

// An Application can record tens of thousands of members; the wire carries
// the first members up to the cap and says the list was cut.
func TestApplyOwnershipCapsARecordedMemberList(t *testing.T) {
	b := ownershiptest.NewBuilder()
	var members []resourceid.Ref
	for i := range maxOwnershipRefs + 20 {
		members = append(members, b.Add("", "ConfigMap", "alpha", fmt.Sprintf("c-%04d", i)).Ref)
	}
	app := b.Manager(ownership.ArgoApplication, "argocd", "app", ownership.MembersRecorded, "alpha", members...)
	w := b.World()
	rel := &Relationships{}
	ApplyOwnership(rel, ownershipView(t, w, ownership.Unrestricted()), fetched(t, w, app.Ref))
	if len(rel.Manages) != maxOwnershipRefs || !rel.Ownership.Manages.Truncated || rel.Ownership.Manages.Membership != "recorded" {
		t.Fatalf("%d manages, coverage %+v; want the first %d recorded, marked truncated", len(rel.Manages), rel.Ownership.Manages, maxOwnershipRefs)
	}
}

// A GitOps manager whose member list can't be read lists the members its
// edges name once each, cut at the cap like a recorded list.
func TestApplyOwnershipCapsLabelMembers(t *testing.T) {
	b := ownershiptest.NewBuilder()
	app := b.Manager(ownership.ArgoApplication, "argocd", "app", ownership.MembersUnreadable, "alpha")
	rel := &Relationships{}
	for i := range maxOwnershipRefs + 20 {
		c := b.Add("", "ConfigMap", "alpha", fmt.Sprintf("c-%04d", i))
		rel.manageTargets = append(rel.manageTargets, refOf(c.Ref), refOf(c.Ref))
	}
	w := b.World()
	ApplyOwnership(rel, ownershipView(t, w, ownership.Unrestricted()), fetched(t, w, app.Ref))
	if len(rel.Manages) != maxOwnershipRefs || !rel.Ownership.Manages.Truncated {
		t.Fatalf("%d manages, coverage %+v; want the first %d, marked truncated", len(rel.Manages), rel.Ownership.Manages, maxOwnershipRefs)
	}
	if got := refsText(rel.Manages); len(slices.Compact(slices.Clone(got))) != len(got) {
		t.Errorf("a member named by two edges is listed twice")
	}
}

// namespacesOnly may read the listed namespaces and every cluster-scoped kind.
type namespacesOnly []string

func (n namespacesOnly) CanRead(_ resourceid.GroupKind, namespace string) ownership.Decision {
	if namespace == "" || slices.Contains(n, namespace) {
		return ownership.Allowed
	}
	return ownership.Denied
}
func (n namespacesOnly) Namespaces() ([]string, bool) { return n, false }

// A cluster-scoped owner's children are searched in each of the caller's
// namespaces; a kind Radar doesn't collect is listed once, not once per
// namespace, and so it is for a namespaced owner.
func TestApplyOwnershipListsAnUnreadKindOnce(t *testing.T) {
	b := ownershiptest.NewBuilder()
	node := b.Add("", "Node", "", "n1")
	b.Add("", "Pod", "alpha", "static-n1", node.OwnerRef(true))
	rs := b.Add("apps", "ReplicaSet", "alpha", "web-1")
	w := b.World()
	widgets := resourceid.GroupKind{Group: "example.io", Kind: "Widget"}
	w.Collection[widgets] = ownershiptest.Collection{State: ownershiptest.NotCollected}
	namespaces := namespacesOnly{"alpha"}
	for i := range 20 {
		namespaces = append(namespaces, fmt.Sprint("team-", i))
	}
	rel := &Relationships{}
	ApplyOwnership(rel, ownershipView(t, w, namespaces), ownership.Observation{Ref: node.Ref, UID: node.UID})
	var unread []OwnershipGap
	for _, g := range rel.Ownership.Children.NotChecked {
		if g.Group == widgets.Group && g.Kind == widgets.Kind {
			unread = append(unread, g)
		}
	}
	if len(unread) != 1 || unread[0].Namespace != "" || unread[0].Reason != string(readreason.RadarKindNotCollected) {
		t.Fatalf("Widget gaps on the Node = %+v; want one, across the caller's namespaces", unread)
	}
	rel = &Relationships{}
	ApplyOwnership(rel, ownershipView(t, w, namespaces), ownership.Observation{Ref: rs.Ref, UID: rs.UID})
	if !slices.Contains(rel.Ownership.Children.NotChecked, OwnershipGap{Group: widgets.Group, Kind: widgets.Kind, Reason: string(readreason.RadarKindNotCollected)}) {
		t.Fatalf("a namespaced owner's gaps = %+v; want Widget, wherever the search looked", rel.Ownership.Children.NotChecked)
	}

	// A kind loading namespace by namespace is a gap in each; the wire lists it
	// once, naming no namespace when it was more than one.
	w.Collection[resourceid.GroupKind{Kind: "Pod"}] = ownershiptest.Collection{State: ownershiptest.Loading, Namespaces: []string{"alpha", "beta"}}
	rel = &Relationships{}
	ApplyOwnership(rel, ownershipView(t, w, namespacesOnly{"alpha", "beta"}), ownership.Observation{Ref: node.Ref, UID: node.UID})
	var loading []OwnershipGap
	for _, g := range rel.Ownership.Children.NotChecked {
		if g.Kind == "Pod" {
			loading = append(loading, g)
		}
	}
	if len(loading) != 1 || loading[0].Namespace != "" || loading[0].Reason != string(readreason.KindSyncPending) {
		t.Fatalf("Pod gaps on the Node = %+v; want one, loading in both namespaces", loading)
	}
}

// The GitOps managers whose member lists name an object are its managers, as
// the managers' lists name them, whatever edges the graph drew: the list of
// a manager that can be read decides, and only a manager whose list can't be
// read is taken from an edge matching the object's labels.
func TestApplyOwnershipTakesGitOpsManagersFromTheirLists(t *testing.T) {
	b := ownershiptest.NewBuilder()
	listed := b.Add("", "ConfigMap", "alpha", "listed")
	labelled := b.Add("apps", "Deployment", "alpha", "labelled")
	recorded := b.Manager(ownership.ArgoApplication, "argocd", "recorded", ownership.MembersRecorded, "alpha", listed.Ref)
	unreadable := b.Manager(ownership.FluxHelmRelease, "alpha", "legacy", ownership.MembersUnreadable, "")
	remote := b.Manager(ownership.FluxKustomization, "flux-system", "remote", ownership.MembersRemote, "", listed.Ref, labelled.Ref)
	w := b.World()
	for _, tc := range []struct {
		name    string
		subject ownershiptest.Object
		edges   []resourceid.Ref
		access  ownership.Access
		want    []string
	}{
		{"listed, with no edge", listed, nil, ownership.Unrestricted(), []string{recorded.Ref.String()}},
		{"an edge the manager's list contradicts", labelled, []resourceid.Ref{recorded.Ref}, ownership.Unrestricted(), nil},
		{"an edge from a manager whose list can't be read", labelled, []resourceid.Ref{unreadable.Ref}, ownership.Unrestricted(), []string{unreadable.Ref.String()}},
		{"an edge from a manager of another cluster", labelled, []resourceid.Ref{remote.Ref}, ownership.Unrestricted(), nil},
		{"listed by a manager the caller may not read", listed, nil, denyKind{ownership.ArgoApplication}, nil},
	} {
		rel := &Relationships{}
		for _, e := range tc.edges {
			rel.manageSources = append(rel.manageSources, refOf(e))
		}
		v := ownershipView(t, w, tc.access)
		ApplyOwnership(rel, v, fetched(t, w, tc.subject.Ref))
		if got := refsText(rel.Managers); !slices.Equal(got, tc.want) {
			t.Errorf("%s: managers %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A gap for the namespaces Radar doesn't collect doesn't let a graph edge
// overrule the list of a manager in a namespace it does collect: the search
// read that list.
func TestApplyOwnershipKeepsAReadListOverAGapElsewhere(t *testing.T) {
	b := ownershiptest.NewBuilder()
	subject := b.Add("", "ConfigMap", "alpha", "cfg")
	// Its list names nothing.
	manager := b.Manager(ownership.ArgoApplication, "alpha", "app", ownership.MembersRecorded, "alpha")
	w := b.World()
	w.Collection[ownership.ArgoApplication] = ownershiptest.Collection{Namespaces: []string{"alpha"}}
	v := ownershipView(t, w, ownership.Unrestricted())
	rel := &Relationships{manageSources: []ResourceRef{refOf(manager.Ref)}}
	ApplyOwnership(rel, v, fetched(t, w, subject.Ref))
	if len(rel.Managers) != 0 {
		t.Errorf("managers %v, want none; gaps %+v", refsText(rel.Managers), v.RecordingManagers(subject.Ref).Gaps)
	}
}

// wholeKindUndecided can't decide whether the caller may read Applications in
// some namespace, though it allows them in alpha.
type wholeKindUndecided struct{ ownership.Access }

func (a wholeKindUndecided) CanRead(gk resourceid.GroupKind, namespace string) ownership.Decision {
	if gk == ownership.ArgoApplication && namespace == "" {
		return ownership.Undecided
	}
	return a.Access.CanRead(gk, namespace)
}

// stalledManagers answers every RecordingManagers search with Applications unread.
type stalledManagers struct{ ownership.ReadSession }

func (stalledManagers) RecordingManagers(resourceid.Ref) ownership.RecordingManagersRead {
	return ownership.RecordingManagersRead{Gaps: []ownership.Gap{{Kind: ownership.ArgoApplication, Reason: readreason.KindSyncPending}}}
}

// A manager whose list the search couldn't read keeps its graph edge, even
// when the gap saying so is one the caller may not read.
func TestApplyOwnershipKeepsTheEdgeOfAManagerBehindAHiddenGap(t *testing.T) {
	b := ownershiptest.NewBuilder()
	cfg := b.Add("", "ConfigMap", "alpha", "cfg")
	app := b.Manager(ownership.ArgoApplication, "alpha", "app", ownership.MembersUnreadable, "alpha")
	w := b.World()
	s, err := ownershiptest.Scan(w).Begin()
	if err != nil {
		t.Fatal(err)
	}
	v := ownership.NewView(stalledManagers{s}, wholeKindUndecided{ownership.Unrestricted()})
	rel := &Relationships{manageSources: []ResourceRef{refOf(app.Ref)}}
	ApplyOwnership(rel, v, fetched(t, w, cfg.Ref))
	if got := refsText(rel.Managers); !slices.Equal(got, []string{app.Ref.String()}) {
		t.Errorf("managers %v, want %v from its edge", got, app.Ref)
	}
}
