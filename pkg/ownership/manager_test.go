package ownership_test

import (
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
			name: "unknown: the walk stops at a controller that's gone",
			build: func(b *ownershiptest.Builder) resourceid.Ref {
				gone := ownershiptest.Object{Ref: resourceid.Ref{Group: "apps", Kind: "ReplicaSet", Namespace: "shop", Name: "web-1"}, APIVersion: "apps/v1", UID: "uid-gone"}
				return b.Add("", "Pod", "shop", "web-1-a", gone.OwnerRef(true)).Ref
			},
			state: ownership.ManagerUnknown,
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
