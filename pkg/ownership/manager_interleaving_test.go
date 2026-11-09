package ownership_test

import (
	"testing"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

// A manager the view read while its kind was loading, then found by the
// recorder search once loaded, leaves the answer incomplete: not none.
func TestManagerKeepsAnEarlierUnavailableRecorderIncomplete(t *testing.T) {
	b := ownershiptest.NewBuilder()
	cm := b.Add("", "ConfigMap", "shop", "settings")
	m := b.Manager(ownership.ArgoApplication, "argocd", "app", ownership.MembersRecorded, "shop", cm.Ref)
	loaded := b.World()
	loading := b.World()
	loading.Collection[ownership.ArgoApplication] = ownershiptest.Collection{State: ownershiptest.Loading}
	s := newCountingSession(t, loading)
	v := ownership.NewView(s, ownership.Unrestricted())
	if got := v.Owner(m.Ref); got.Subject.Read.State != ownership.Unavailable {
		t.Fatal("scenario: the manager must first read unavailable")
	}
	later, err := ownershiptest.Scan(loaded).Begin()
	if err != nil {
		t.Fatal(err)
	}
	s.now = later
	if got := v.Manager(cm.Ref); got.State != ownership.ManagerUnknown {
		t.Fatalf("state %s, direct %+v; want unknown", got.State, got.Direct)
	}
}

// The same through the wire projection, for an object whose manager is also
// a non-controller owner the view reads first.
func TestApplyOwnershipKeepsAnEarlierUnavailableRecorderIncomplete(t *testing.T) {
	b := ownershiptest.NewBuilder()
	cm := b.Add("", "ConfigMap", "shop", "settings")
	m := b.Manager(ownership.ArgoApplication, "shop", "app", ownership.MembersRecorded, "shop", cm.Ref)
	loaded := b.World()
	for i := range loaded.Objects {
		if loaded.Objects[i].Ref == cm.Ref {
			loaded.Objects[i].OwnerReferences = append(loaded.Objects[i].OwnerReferences, m.OwnerRef(false))
		}
	}
	loading := b.World()
	loading.Collection[ownership.ArgoApplication] = ownershiptest.Collection{State: ownershiptest.Loading}
	s := newCountingSession(t, loading)
	v := ownership.NewView(s, ownership.Unrestricted())
	fetched := s.Object(cm.Ref).Object
	v.Seed(fetched)
	later, err := ownershiptest.Scan(loaded).Begin()
	if err != nil {
		t.Fatal(err)
	}
	s.later, s.switchAt = later, "Object"
	rel := &topology.Relationships{}
	topology.ApplyOwnership(rel, v, fetched)
	if got := rel.Ownership.Manager; got == nil || got.State != "unknown" {
		t.Fatalf("manager %+v; want unknown", got)
	}
}

// A manager replaced since the view read it can't confirm membership for the
// incarnation the view holds, as Members already refuses.
func TestManagerRejectsARecordersDifferentIncarnation(t *testing.T) {
	b := ownershiptest.NewBuilder()
	cm := b.Meta(b.Add("", "ConfigMap", "shop", "settings"), nil, map[string]string{trackingID: "argocd_app:/ConfigMap:shop/settings"})
	m := b.Manager(ownership.ArgoApplication, "argocd", "app", ownership.MembersRecorded, "shop")
	w := b.World()
	s := newCountingSession(t, w)
	v := ownership.NewView(s, ownership.Unrestricted())
	v.Owner(m.Ref)
	changeManager(t, s, w, m.Ref, func(o *ownershiptest.Object) {
		o.UID = "replacement-uid"
		o.Members = []resourceid.Ref{cm.Ref}
	})
	if members := v.Members(m.Ref); members.Membership == ownership.MembersRecorded || len(members.Gaps) == 0 {
		t.Fatalf("scenario: Members must refuse the new incarnation: %+v", members)
	}
	got := v.Manager(cm.Ref)
	if got.State == ownership.ManagerAgree || checkOf(got.Direct, app("app")) != ownership.ClaimUnchecked {
		t.Fatalf("state %s, claims %+v; want the claim unchecked, membership unconfirmed", got.State, got.Direct.Claims)
	}
}

// Falling back to the nearest ancestor's claim keeps the claims farther up.
func TestManagerFallbackKeepsClaimsFartherUp(t *testing.T) {
	b := ownershiptest.NewBuilder()
	op := b.Add("example.io", "Widget", "shop", "op")
	d := b.Meta(b.Add("apps", "Deployment", "shop", "web", op.OwnerRef(true)), map[string]string{instanceLabel: "argocd_far"}, nil)
	rs := b.Meta(b.Add("apps", "ReplicaSet", "shop", "web-1", d.OwnerRef(true)), map[string]string{instanceLabel: "argocd_near"}, nil)
	pod := b.Add("", "Pod", "shop", "web-1-a", rs.OwnerRef(true))
	got := view(t, b.World(), ownership.Unrestricted()).Manager(pod.Ref)
	if got.State != ownership.ManagerClaimed || got.Via == nil || got.Via.Object != rs.Ref {
		t.Fatalf("answer %+v; want claimed via the ReplicaSet", got)
	}
	if len(got.Between) != 1 || got.Between[0].Object != d.Ref {
		t.Fatalf("between %+v; want the Deployment's claim kept", got.Between)
	}
}

// A claim naming no namespace, with no listing manager of its name, is
// unresolved: Radar says nothing of whether such a manager exists.
func TestManagerUnqualifiedClaimWithoutAMatchIsUnresolved(t *testing.T) {
	b := ownershiptest.NewBuilder()
	cm := b.Meta(b.Add("", "ConfigMap", "shop", "settings"), map[string]string{instanceLabel: "elsewhere"}, nil)
	got := view(t, b.World(), ownership.Unrestricted()).Manager(cm.Ref)
	unqualified := resourceid.Ref{Group: "argoproj.io", Kind: "Application", Name: "elsewhere"}
	if got.State != ownership.ManagerClaimed || checkOf(got.Direct, unqualified) != ownership.ClaimUnresolved {
		t.Fatalf("answer %+v; want claimed with the claim unresolved", got)
	}
}
