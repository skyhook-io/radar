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
