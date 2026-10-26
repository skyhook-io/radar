package resourcecontext

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/topology"
)

func controlledPod() *corev1.Pod {
	yes := true
	return &corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web-0", UID: "uid-pod", OwnerReferences: []metav1.OwnerReference{
			{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-1", UID: "uid-rs", Controller: &yes},
		}},
	}
}

// An ownership answer that names no owner says why in the context, as the
// graph's answer did: the reader may not view it, or Radar couldn't tell.
func TestBuildSaysWhyOwnershipNamedNoOwner(t *testing.T) {
	for _, tc := range []struct {
		name     string
		coverage topology.OwnershipCoverage
		want     OmittedReason
	}{
		{"withheld", topology.OwnershipCoverage{PrimaryOwnerState: "withheld"}, OmittedRBACDenied},
		{"unknown", topology.OwnershipCoverage{PrimaryOwnerState: "unknown"}, OmittedUnavailable},
		{"unavailable", topology.OwnershipCoverage{UnavailableReason: "connection_changed"}, OmittedUnavailable},
		{"gone", topology.OwnershipCoverage{PrimaryOwnerState: "gone"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rc := Build(context.Background(), controlledPod(), Options{
				AccessChecker: allowAllChecker{},
				Ownership: func(rel *topology.Relationships, _ ownership.Observation) {
					c := tc.coverage
					rel.Ownership = &c
				},
			})
			if rc.Owner != nil {
				t.Fatalf("owner = %+v; the ownership answer names none", rc.Owner)
			}
			var got OmittedReason
			for _, o := range rc.Omitted {
				if o.Field == "owner" {
					got = o.Reason
				}
			}
			if got != tc.want {
				t.Fatalf("owner omitted for %q, want %q (omitted %+v)", got, tc.want, rc.Omitted)
			}
		})
	}
}

// Ownership doesn't follow an Event's owner references, so an Event's context
// names no owner, rather than the unchecked one its own reference names.
func TestBuildNamesNoOwnerForAnEvent(t *testing.T) {
	event := &corev1.Event{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Event"}, ObjectMeta: metav1.ObjectMeta{
		Namespace: "shop", Name: "e", UID: "uid-event", OwnerReferences: controlledPod().OwnerReferences,
	}}
	s, err := ownershiptest.Scan(ownershiptest.Fixture()).Begin()
	if err != nil {
		t.Fatal(err)
	}
	v := ownership.NewView(s, ownership.Unrestricted())
	rc := Build(context.Background(), event, Options{
		AccessChecker: allowAllChecker{},
		Ownership:     func(rel *topology.Relationships, fetched ownership.Observation) { topology.ApplyOwner(rel, v, fetched) },
	})
	if rc.Owner != nil {
		t.Fatalf("owner = %+v; ownership names none for an Event", rc.Owner)
	}
}

// Without a graph, the managers an object's own labels and annotations name
// still come through when ownership is answered.
func TestBuildKeepsManagedByWithoutTopology(t *testing.T) {
	deploy := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web", UID: "uid-deploy", Annotations: map[string]string{
			"argocd.argoproj.io/tracking-id": "storefront:apps/Deployment:shop/web",
		}},
	}
	without := Build(context.Background(), deploy, Options{AccessChecker: allowAllChecker{}})
	with := Build(context.Background(), deploy, Options{AccessChecker: allowAllChecker{}, Ownership: func(rel *topology.Relationships, _ ownership.Observation) {
		rel.Ownership = &topology.OwnershipCoverage{}
	}})
	if len(without.ManagedBy) == 0 {
		t.Fatal("scenario: the tracking annotation must name a manager without ownership")
	}
	if len(with.ManagedBy) != len(without.ManagedBy) || with.ManagedBy[0] != without.ManagedBy[0] {
		t.Fatalf("managedBy with ownership = %+v, without = %+v", with.ManagedBy, without.ManagedBy)
	}
}

// The manager answer reaches the resource context compacted: the evidence
// that answers, the controller's when it does, and no copied claim.
func TestBuildCompactsTheManagerAnswer(t *testing.T) {
	pod := &corev1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web-1-a", UID: "uid-pod"},
	}
	app := topology.ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "shop"}
	other := topology.ResourceRef{Group: "argoproj.io", Kind: "Application", Name: "old"}
	deploy := topology.ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "web"}
	rc := Build(context.Background(), pod, Options{AccessChecker: allowAllChecker{}, Ownership: func(rel *topology.Relationships, _ ownership.Observation) {
		rel.Ownership = &topology.OwnershipCoverage{Manager: &topology.ManagerAnswer{
			State: "agree", Lead: &app, Narrowed: true,
			Direct: topology.ManagerEvidence{Object: topology.ResourceRef{Kind: "Pod", Namespace: "shop", Name: "web-1-a"}, Claims: []topology.ManagerClaim{
				{Manager: topology.ResourceRef{Group: "argoproj.io", Kind: "Application", Name: "tmpl"}, Source: "argo-instance-label", Check: "unresolved", Weak: true},
			}},
			Via: &topology.ManagerEvidence{Object: deploy, Recorders: []topology.ResourceRef{app}, Claims: []topology.ManagerClaim{
				{Manager: app, Source: "argo-tracking-id", Check: "recorded"},
				{Manager: other, Source: "argo-tracking-id", Check: "missing", Copied: true},
			}},
		}}
	}})
	m := rc.Manager
	if m == nil || m.State != "agree" || m.Lead == nil || m.Lead.Name != "shop" || m.Via == nil || m.Via.Name != "web" || !m.Narrowed {
		t.Fatalf("manager %+v; want agree, led by shop, via the Deployment, narrowed", m)
	}
	if len(m.ListedBy) != 1 || len(m.Claims) != 1 || m.Claims[0].Check != "recorded" {
		t.Fatalf("listedBy %+v claims %+v; want the Deployment's evidence without the copied claim", m.ListedBy, m.Claims)
	}
	if len(m.AlsoClaimed) != 1 || m.AlsoClaimed[0].Manager.Name != "tmpl" || m.AlsoClaimed[0].On == nil || m.AlsoClaimed[0].On.Kind != "Pod" {
		t.Fatalf("alsoClaimed %+v; want the Pod's own weak claim, on the Pod", m.AlsoClaimed)
	}
}

// A claim the walk passed is kept even when no ancestor answers.
func TestBuildKeepsClaimsOnTheWayWithoutVia(t *testing.T) {
	pod := &corev1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web-1-a", UID: "uid-pod"},
	}
	rs := topology.ResourceRef{Group: "apps", Kind: "ReplicaSet", Namespace: "shop", Name: "web-1"}
	rc := Build(context.Background(), pod, Options{AccessChecker: allowAllChecker{}, Ownership: func(rel *topology.Relationships, _ ownership.Observation) {
		rel.Ownership = &topology.OwnershipCoverage{Manager: &topology.ManagerAnswer{
			State:   "claimed",
			Direct:  topology.ManagerEvidence{Object: topology.ResourceRef{Kind: "Pod", Namespace: "shop", Name: "web-1-a"}, Claims: []topology.ManagerClaim{{Manager: topology.ResourceRef{Group: "argoproj.io", Kind: "Application", Name: "pod-app"}, Source: "argo-instance-label", Check: "unresolved", Weak: true}}},
			Between: []topology.ManagerEvidence{{Object: rs, Claims: []topology.ManagerClaim{{Manager: topology.ResourceRef{Group: "argoproj.io", Kind: "Application", Name: "rs-app"}, Source: "argo-instance-label", Check: "unresolved", Weak: true}}}},
		}}
	}})
	m := rc.Manager
	if m == nil || m.Via != nil || len(m.AlsoClaimed) != 1 || m.AlsoClaimed[0].Manager.Name != "rs-app" || m.AlsoClaimed[0].On == nil || m.AlsoClaimed[0].On.Kind != "ReplicaSet" {
		t.Fatalf("manager %+v; want the ReplicaSet's claim in alsoClaimed", m)
	}
}
