package ownership_test

import (
	"fmt"
	"testing"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

const (
	trackingID    = "argocd.argoproj.io/tracking-id"
	instanceLabel = "argocd.argoproj.io/instance"
	helmRelease   = "meta.helm.sh/release-name"
	helmNamespace = "meta.helm.sh/release-namespace"
)

func app(name string) resourceid.Ref {
	return resourceid.Ref{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: name}
}

// checkOf returns the check of the claim naming manager.
func checkOf(e ownership.ManagerEvidence, manager resourceid.Ref) ownership.ClaimCheck {
	for _, c := range e.Claims {
		if c.Manager == manager {
			return c.Check
		}
	}
	return ""
}

func TestManagerStates(t *testing.T) {
	recorded := ownership.MembersRecorded
	for _, tc := range []struct {
		name  string
		build func(b *ownershiptest.Builder) resourceid.Ref
		state ownership.ManagerState
		lead  *resourceid.Ref
		check func(t *testing.T, a ownership.ManagerAnswer)
	}{
		{
			name: "listed: a list names it, its metadata names no one",
			build: func(b *ownershiptest.Builder) resourceid.Ref {
				cm := b.Add("", "ConfigMap", "shop", "settings")
				b.Manager(ownership.ArgoApplication, "argocd", "shop", recorded, "shop", cm.Ref)
				return cm.Ref
			},
			state: ownership.ManagerListed, lead: ref(app("shop")),
		},
		{
			name: "agree: its tracking ID names the manager whose list names it",
			build: func(b *ownershiptest.Builder) resourceid.Ref {
				cm := b.Meta(b.Add("", "ConfigMap", "shop", "settings"), nil, map[string]string{trackingID: "argocd_shop:/ConfigMap:shop/settings"})
				b.Manager(ownership.ArgoApplication, "argocd", "shop", recorded, "shop", cm.Ref)
				return cm.Ref
			},
			state: ownership.ManagerAgree, lead: ref(app("shop")),
		},
		{
			// A label without a namespace matches the Application of its name
			// whose list names the object.
			name: "agree: an instance label without a namespace",
			build: func(b *ownershiptest.Builder) resourceid.Ref {
				cm := b.Meta(b.Add("", "ConfigMap", "shop", "settings"), map[string]string{instanceLabel: "shop"}, nil)
				b.Manager(ownership.ArgoApplication, "argocd", "shop", recorded, "shop", cm.Ref)
				return cm.Ref
			},
			state: ownership.ManagerAgree, lead: ref(app("shop")),
		},
		{
			name: "disagree: its label names a manager whose readable list omits it",
			build: func(b *ownershiptest.Builder) resourceid.Ref {
				cm := b.Meta(b.Add("", "ConfigMap", "shop", "settings"), nil, map[string]string{trackingID: "argocd_old:/ConfigMap:shop/settings"})
				b.Manager(ownership.ArgoApplication, "argocd", "old", recorded, "shop")
				b.Manager(ownership.ArgoApplication, "argocd", "shop", recorded, "shop", cm.Ref)
				return cm.Ref
			},
			state: ownership.ManagerDisagree,
			check: func(t *testing.T, a ownership.ManagerAnswer) {
				if got := checkOf(a.Direct, app("old")); got != ownership.ClaimOmitted {
					t.Errorf("claim of old: %q, want omitted", got)
				}
			},
		},
		{
			// The claimed manager's list can't be read; still a disagreement.
			name: "disagree: a list names it and its label names an unreadable manager",
			build: func(b *ownershiptest.Builder) resourceid.Ref {
				cm := b.Meta(b.Add("", "ConfigMap", "shop", "settings"), nil, map[string]string{trackingID: "argocd_old:/ConfigMap:shop/settings"})
				b.Manager(ownership.ArgoApplication, "argocd", "old", ownership.MembersUnreadable, "shop")
				b.Manager(ownership.ArgoApplication, "argocd", "shop", recorded, "shop", cm.Ref)
				return cm.Ref
			},
			state: ownership.ManagerDisagree,
			check: func(t *testing.T, a ownership.ManagerAnswer) {
				if got := checkOf(a.Direct, app("old")); got != ownership.ClaimUnreadable {
					t.Errorf("claim of old: %q, want unreadable", got)
				}
			},
		},
		{
			name: "shared: two lists name it",
			build: func(b *ownershiptest.Builder) resourceid.Ref {
				cm := b.Add("", "ConfigMap", "shop", "settings")
				b.Manager(ownership.ArgoApplication, "argocd", "a", recorded, "shop", cm.Ref)
				b.Manager(ownership.ArgoApplication, "argocd", "b", recorded, "shop", cm.Ref)
				return cm.Ref
			},
			state: ownership.ManagerShared,
		},
		{
			name: "shared: the claimed one of two leads",
			build: func(b *ownershiptest.Builder) resourceid.Ref {
				cm := b.Meta(b.Add("", "ConfigMap", "shop", "settings"), nil, map[string]string{trackingID: "argocd_b:/ConfigMap:shop/settings"})
				b.Manager(ownership.ArgoApplication, "argocd", "a", recorded, "shop", cm.Ref)
				b.Manager(ownership.ArgoApplication, "argocd", "b", recorded, "shop", cm.Ref)
				return cm.Ref
			},
			state: ownership.ManagerShared, lead: ref(app("b")),
		},
		{
			name: "claimed: the claimed manager's list can't be read",
			build: func(b *ownershiptest.Builder) resourceid.Ref {
				cm := b.Meta(b.Add("", "ConfigMap", "shop", "settings"), nil, map[string]string{trackingID: "argocd_shop:/ConfigMap:shop/settings"})
				b.Manager(ownership.ArgoApplication, "argocd", "shop", ownership.MembersUnreadable, "shop")
				return cm.Ref
			},
			state: ownership.ManagerClaimed, lead: ref(app("shop")),
			check: func(t *testing.T, a ownership.ManagerAnswer) {
				if got := checkOf(a.Direct, app("shop")); got != ownership.ClaimUnreadable {
					t.Errorf("check %q, want unreadable", got)
				}
			},
		},
		{
			name: "claimed: the claimed manager applies elsewhere",
			build: func(b *ownershiptest.Builder) resourceid.Ref {
				cm := b.Meta(b.Add("", "ConfigMap", "shop", "settings"), nil, map[string]string{trackingID: "argocd_shop:/ConfigMap:shop/settings"})
				b.Manager(ownership.ArgoApplication, "argocd", "shop", ownership.MembersRemote, "shop", cm.Ref)
				return cm.Ref
			},
			state: ownership.ManagerClaimed, lead: ref(app("shop")),
			check: func(t *testing.T, a ownership.ManagerAnswer) {
				if got := checkOf(a.Direct, app("shop")); got != ownership.ClaimRemote {
					t.Errorf("check %q, want remote", got)
				}
			},
		},
		{
			// A readable list that omits it, and no other: the claim stands
			// unconfirmed rather than turning into none.
			name: "claimed: the claimed manager's readable list omits it",
			build: func(b *ownershiptest.Builder) resourceid.Ref {
				cm := b.Meta(b.Add("", "ConfigMap", "shop", "settings"), nil, map[string]string{trackingID: "argocd_shop:/ConfigMap:shop/settings"})
				b.Manager(ownership.ArgoApplication, "argocd", "shop", recorded, "shop")
				return cm.Ref
			},
			state: ownership.ManagerClaimed, lead: ref(app("shop")),
			check: func(t *testing.T, a ownership.ManagerAnswer) {
				if got := checkOf(a.Direct, app("shop")); got != ownership.ClaimOmitted {
					t.Errorf("check %q, want omitted", got)
				}
			},
		},
		{
			name: "claimed: no such manager",
			build: func(b *ownershiptest.Builder) resourceid.Ref {
				return b.Meta(b.Add("", "ConfigMap", "shop", "settings"), nil, map[string]string{trackingID: "argocd_gone:/ConfigMap:shop/settings"}).Ref
			},
			state: ownership.ManagerClaimed, lead: ref(app("gone")),
			check: func(t *testing.T, a ownership.ManagerAnswer) {
				if got := checkOf(a.Direct, app("gone")); got != ownership.ClaimMissing {
					t.Errorf("check %q, want missing", got)
				}
			},
		},
		{
			name: "claimed: a native Helm release",
			build: func(b *ownershiptest.Builder) resourceid.Ref {
				return b.Meta(b.Add("", "ConfigMap", "shop", "settings"), nil, map[string]string{helmRelease: "web", helmNamespace: "shop"}).Ref
			},
			state: ownership.ManagerClaimed, lead: &resourceid.Ref{Kind: "HelmRelease", Namespace: "shop", Name: "web"},
		},
		{
			// Flux's helm-controller and Argo CD installs carry the Helm
			// release's annotations; they don't make a second manager.
			name: "listed: Helm annotations beside a list naming it",
			build: func(b *ownershiptest.Builder) resourceid.Ref {
				cm := b.Meta(b.Add("", "ConfigMap", "shop", "settings"), nil, map[string]string{helmRelease: "web", helmNamespace: "shop"})
				b.Manager(ownership.ArgoApplication, "argocd", "shop", recorded, "shop", cm.Ref)
				return cm.Ref
			},
			state: ownership.ManagerListed, lead: ref(app("shop")),
		},
		{
			// A tracking ID and an instance label name one Application, the
			// ID saying its namespace.
			name: "claimed: a tracking ID and an instance label naming one Application",
			build: func(b *ownershiptest.Builder) resourceid.Ref {
				return b.Meta(b.Add("", "ConfigMap", "shop", "settings"),
					map[string]string{instanceLabel: "shop"},
					map[string]string{trackingID: "argocd_shop:/ConfigMap:shop/settings"}).Ref
			},
			state: ownership.ManagerClaimed, lead: ref(app("shop")),
		},
		{
			name: "claimed: two claims naming different managers, no list",
			build: func(b *ownershiptest.Builder) resourceid.Ref {
				return b.Meta(b.Add("", "ConfigMap", "shop", "settings"),
					map[string]string{"kustomize.toolkit.fluxcd.io/name": "apps", "kustomize.toolkit.fluxcd.io/namespace": "flux-system"},
					map[string]string{trackingID: "argocd_shop:/ConfigMap:shop/settings"}).Ref
			},
			state: ownership.ManagerClaimed,
		},
		{
			name: "none: copied tracking ID only, everything read",
			build: func(b *ownershiptest.Builder) resourceid.Ref {
				return b.Meta(b.Add("", "ConfigMap", "shop", "copy"), nil, map[string]string{trackingID: "argocd_shop:/ConfigMap:shop/settings"}).Ref
			},
			state: ownership.ManagerNone,
		},
		{
			name: "unknown: no evidence, and a list Radar can't read",
			build: func(b *ownershiptest.Builder) resourceid.Ref {
				cm := b.Add("", "ConfigMap", "shop", "settings")
				b.Manager(ownership.ArgoApplication, "argocd", "broken", ownership.MembersUnreadable, "shop")
				return cm.Ref
			},
			state: ownership.ManagerUnknown,
		},
		{
			name: "none: no evidence anywhere",
			build: func(b *ownershiptest.Builder) resourceid.Ref {
				return b.Add("", "ConfigMap", "shop", "settings").Ref
			},
			state: ownership.ManagerNone,
		},
		{
			// The fleet's commonest case: the Pod inherits its template's
			// instance label; only its Deployment is listed.
			name: "via: a Pod whose Deployment a list names",
			build: func(b *ownershiptest.Builder) resourceid.Ref {
				d := b.Add("apps", "Deployment", "shop", "web")
				rs := b.Add("apps", "ReplicaSet", "shop", "web-1", d.OwnerRef(true))
				pod := b.Meta(b.Add("", "Pod", "shop", "web-1-a", rs.OwnerRef(true)), map[string]string{instanceLabel: "shop"}, nil)
				b.Manager(ownership.ArgoApplication, "argocd", "shop", recorded, "shop", d.Ref)
				return pod.Ref
			},
			state: ownership.ManagerListed, lead: ref(app("shop")),
			check: func(t *testing.T, a ownership.ManagerAnswer) {
				if a.Via == nil || a.Via.Object.Kind != "Deployment" {
					t.Fatalf("via %+v, want the Deployment", a.Via)
				}
				if len(a.Direct.Claims) != 1 || !a.Direct.Claims[0].Weak {
					t.Errorf("direct claims %+v, want the Pod's label kept as weak", a.Direct.Claims)
				}
			},
		},
		{
			// A Deployment copies its annotations, a Helm release's among them,
			// onto its ReplicaSets; the list naming the Deployment answers.
			name: "via: a ReplicaSet with its Deployment's Helm annotations",
			build: func(b *ownershiptest.Builder) resourceid.Ref {
				helm := map[string]string{helmRelease: "web", helmNamespace: "shop"}
				d := b.Meta(b.Add("apps", "Deployment", "shop", "web"), nil, helm)
				rs := b.Meta(b.Add("apps", "ReplicaSet", "shop", "web-1", d.OwnerRef(true)), nil, helm)
				b.Manager(ownership.ArgoApplication, "argocd", "shop", recorded, "shop", d.Ref)
				return rs.Ref
			},
			state: ownership.ManagerListed, lead: ref(app("shop")),
		},
		{
			// A self-referencing tracking ID on the object is its own answer,
			// whatever manages its controller.
			name: "direct: an object's own tracking ID stops the walk",
			build: func(b *ownershiptest.Builder) resourceid.Ref {
				d := b.Add("apps", "Deployment", "shop", "web")
				rs := b.Meta(b.Add("apps", "ReplicaSet", "shop", "web-1", d.OwnerRef(true)), nil, map[string]string{trackingID: "argocd_other:apps/ReplicaSet:shop/web-1"})
				b.Manager(ownership.ArgoApplication, "argocd", "shop", recorded, "shop", d.Ref)
				b.Manager(ownership.ArgoApplication, "argocd", "other", ownership.MembersUnreadable, "shop")
				return rs.Ref
			},
			state: ownership.ManagerClaimed, lead: ref(app("other")),
			check: func(t *testing.T, a ownership.ManagerAnswer) {
				if a.Via != nil {
					t.Errorf("via %+v, want none", a.Via)
				}
			},
		},
		{
			// A gone controller ends the chain with an answer: nothing above
			// it was missed.
			name: "none: the walk stops at a controller that's gone",
			build: func(b *ownershiptest.Builder) resourceid.Ref {
				gone := ownershiptest.Object{Ref: resourceid.Ref{Group: "apps", Kind: "ReplicaSet", Namespace: "shop", Name: "web-1"}, APIVersion: "apps/v1", UID: "uid-gone"}
				return b.Add("", "Pod", "shop", "web-1-a", gone.OwnerRef(true)).Ref
			},
			state: ownership.ManagerNone,
			check: func(t *testing.T, a ownership.ManagerAnswer) {
				if a.ChainEnd != ownership.ChainEndUnfollowed {
					t.Errorf("chain end %q, want unfollowed", a.ChainEnd)
				}
			},
		},
		{
			// Each manager answers for what its own list names: the parent
			// for the child Application, not for the child's members.
			name: "app of apps: a child Application is managed by its parent",
			build: func(b *ownershiptest.Builder) resourceid.Ref {
				child := b.Manager(ownership.ArgoApplication, "argocd", "child", recorded, "shop")
				b.Manager(ownership.ArgoApplication, "argocd", "parent", recorded, "argocd", child.Ref)
				return child.Ref
			},
			state: ownership.ManagerListed, lead: ref(app("parent")),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := ownershiptest.NewBuilder()
			subject := tc.build(b)
			got := view(t, b.World(), ownership.Unrestricted()).Manager(subject)
			if got.State != tc.state {
				t.Fatalf("state %q, want %q (direct %+v, via %+v)", got.State, tc.state, got.Direct, got.Via)
			}
			if (got.Lead == nil) != (tc.lead == nil) || got.Lead != nil && *got.Lead != *tc.lead {
				t.Fatalf("lead %v, want %v", got.Lead, tc.lead)
			}
			if got.Narrowed {
				t.Error("narrowed for an unrestricted caller")
			}
			if tc.check != nil {
				tc.check(t, got)
			}
		})
	}
}

// A caller who may not read Applications sees the claim, but nothing of the
// manager it names, and an answer marked narrowed.
func TestManagerForACallerWhoMayNotReadManagers(t *testing.T) {
	b := ownershiptest.NewBuilder()
	cm := b.Meta(b.Add("", "ConfigMap", "shop", "settings"), nil, map[string]string{trackingID: "argocd_shop:/ConfigMap:shop/settings"})
	b.Manager(ownership.ArgoApplication, "argocd", "shop", ownership.MembersRecorded, "shop", cm.Ref)
	got := view(t, b.World(), access{denied: []resourceid.GroupKind{ownership.ArgoApplication}}).Manager(cm.Ref)
	if got.State != ownership.ManagerClaimed || len(got.Direct.Recorders) != 0 || !got.Narrowed {
		t.Fatalf("answer %+v; want claimed, no recorders, narrowed", got)
	}
	if c := checkOf(got.Direct, app("shop")); c != ownership.ClaimWithheld {
		t.Fatalf("check %q, want withheld", c)
	}
}

func ref(r resourceid.Ref) *resourceid.Ref { return &r }

// Every claim must name the one recorder for agreement: one that names
// another manager makes a disagreement, whichever source it comes from.
func TestManagerAgreementNeedsEveryClaim(t *testing.T) {
	b := ownershiptest.NewBuilder()
	cm := b.Meta(b.Add("", "ConfigMap", "shop", "settings"),
		map[string]string{"kustomize.toolkit.fluxcd.io/name": "apps", "kustomize.toolkit.fluxcd.io/namespace": "flux-system"},
		map[string]string{trackingID: "argocd_shop:/ConfigMap:shop/settings"})
	b.Manager(ownership.ArgoApplication, "argocd", "shop", ownership.MembersRecorded, "shop", cm.Ref)
	if got := view(t, b.World(), ownership.Unrestricted()).Manager(cm.Ref); got.State != ownership.ManagerDisagree || got.Lead != nil {
		t.Fatalf("answer %+v; want a disagreement with no lead", got)
	}
}

// Weak claims at each hop let the walk go on, and are kept on the way.
func TestManagerKeepsWeakEvidenceAlongTheWalk(t *testing.T) {
	b := ownershiptest.NewBuilder()
	d := b.Add("apps", "Deployment", "shop", "web")
	rs := b.Meta(b.Add("apps", "ReplicaSet", "shop", "web-1", d.OwnerRef(true)), map[string]string{instanceLabel: "other"}, nil)
	pod := b.Add("", "Pod", "shop", "web-1-a", rs.OwnerRef(true))
	b.Manager(ownership.ArgoApplication, "argocd", "shop", ownership.MembersRecorded, "shop", d.Ref)
	got := view(t, b.World(), ownership.Unrestricted()).Manager(pod.Ref)
	if got.State != ownership.ManagerListed || got.Via == nil || got.Via.Object != d.Ref {
		t.Fatalf("answer %+v; want listed via the Deployment", got)
	}
	if len(got.Between) != 1 || got.Between[0].Object != rs.Ref || !got.Between[0].Claims[0].Weak {
		t.Fatalf("between %+v; want the ReplicaSet's weak claim", got.Between)
	}
}

// When no object settles it, a claim the walk passed answers: the Pod is
// tracked through its ReplicaSet, not unmanaged.
func TestManagerFallsBackToAWeakClaimOnTheWay(t *testing.T) {
	b := ownershiptest.NewBuilder()
	d := b.Add("apps", "Deployment", "shop", "web")
	rs := b.Meta(b.Add("apps", "ReplicaSet", "shop", "web-1", d.OwnerRef(true)), map[string]string{instanceLabel: "argocd_shop"}, nil)
	pod := b.Add("", "Pod", "shop", "web-1-a", rs.OwnerRef(true))
	got := view(t, b.World(), ownership.Unrestricted()).Manager(pod.Ref)
	if got.State != ownership.ManagerClaimed || got.Via == nil || got.Via.Object != rs.Ref || len(got.Between) != 0 {
		t.Fatalf("answer %+v; want claimed via the ReplicaSet", got)
	}
}

// A controller the caller may not read stops the walk: the answer says so,
// and nothing of the controller.
func TestManagerStopsAtAWithheldController(t *testing.T) {
	b := ownershiptest.NewBuilder()
	d := b.Add("apps", "Deployment", "shop", "web")
	rs := b.Add("apps", "ReplicaSet", "shop", "web-1", d.OwnerRef(true))
	pod := b.Add("", "Pod", "shop", "web-1-a", rs.OwnerRef(true))
	b.Manager(ownership.ArgoApplication, "argocd", "shop", ownership.MembersRecorded, "shop", d.Ref)
	got := view(t, b.World(), access{denied: []resourceid.GroupKind{{Group: "apps", Kind: "ReplicaSet"}}}).Manager(pod.Ref)
	if got.State != ownership.ManagerUnknown || got.ChainEnd != ownership.ChainEndUnfollowed || got.Stop == nil || got.Stop.Owner.State != ownership.OwnerWithheld || got.Via != nil {
		t.Fatalf("answer %+v; want unknown, stopped at a withheld controller", got)
	}
}

// Radar couldn't tell whether the caller may read Applications everywhere
// they are loading: no evidence is unknown, not none.
func TestManagerWithUndecidedAccessIsUnknown(t *testing.T) {
	b := ownershiptest.NewBuilder()
	cm := b.Add("", "ConfigMap", "shop", "settings")
	w := b.World()
	c := w.Collection[ownership.ArgoApplication]
	c.State = ownershiptest.Loading
	w.Collection[ownership.ArgoApplication] = c
	got := view(t, w, wholeKindUndecided{ownership.Unrestricted()}).Manager(cm.Ref)
	if got.State != ownership.ManagerUnknown || !got.Direct.Undecided {
		t.Fatalf("answer %+v; want unknown with undecided access", got)
	}
}

// A controller of a kind discovery can't map stops the walk; the stop keeps
// the reference that names it, from the metadata of an object the caller reads.
func TestManagerStopKeepsAnUnmappedControllersReference(t *testing.T) {
	b := ownershiptest.NewBuilder()
	parent := ownershiptest.Object{Ref: resourceid.Ref{Group: "missing.example.io", Kind: "Parent", Namespace: "shop", Name: "controller-x"}, APIVersion: "missing.example.io/v1", UID: "uid-parent"}
	pod := b.Add("", "Pod", "shop", "web-1-a", parent.OwnerRef(true))
	got := view(t, b.World(), ownership.Unrestricted()).Manager(pod.Ref)
	if got.Stop == nil || got.Stop.Owner.State != ownership.OwnerUnknown || got.Stop.Reference.Kind != "Parent" || got.Stop.Reference.Name != "controller-x" {
		t.Fatalf("stop %+v; want the unmapped Parent's reference", got.Stop)
	}
}

// Checking a stale claim against a large manager reads that manager's list,
// not every object in it.
func TestManagerChecksAClaimWithoutReadingItsManagersMembers(t *testing.T) {
	b := ownershiptest.NewBuilder()
	var members []resourceid.Ref
	for i := range 50 {
		members = append(members, b.Add("", "ConfigMap", "shop", fmt.Sprint("listed-", i)).Ref)
	}
	cm := b.Meta(b.Add("", "ConfigMap", "shop", "stale"), nil, map[string]string{trackingID: "argocd_shop:/ConfigMap:shop/stale"})
	b.Manager(ownership.ArgoApplication, "argocd", "shop", ownership.MembersRecorded, "shop", members...)
	s, err := ownershiptest.Scan(b.World()).Begin()
	if err != nil {
		t.Fatal(err)
	}
	counted := &objectCounter{ReadSession: s}
	got := ownership.NewView(counted, ownership.Unrestricted()).Manager(cm.Ref)
	if got.State != ownership.ManagerClaimed || checkOf(got.Direct, app("shop")) != ownership.ClaimOmitted {
		t.Fatalf("answer %+v; want the claim checked as omitted", got)
	}
	if counted.objects > 3 {
		t.Fatalf("%d object reads; want the subject and the manager, not the manager's members", counted.objects)
	}
}

type objectCounter struct {
	ownership.ReadSession
	objects int
}

func (s *objectCounter) Object(ref resourceid.Ref) ownership.ObjectRead {
	s.objects++
	return s.ReadSession.Object(ref)
}
