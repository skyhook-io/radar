package ownership_test

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func object(t *testing.T, w ownershiptest.World, group, kind, namespace, name string) ownershiptest.Object {
	t.Helper()
	ref := resourceid.Ref{Group: group, Kind: kind, Namespace: namespace, Name: name}
	for _, o := range w.Objects {
		if o.Ref == ref {
			return o
		}
	}
	t.Fatalf("no %v in the World", ref)
	return ownershiptest.Object{}
}

// resolve resolves the dependent's owner references in a scan of w.
func resolve(t *testing.T, w ownershiptest.World, dependent ownershiptest.Object) []ownership.OwnerResolution {
	t.Helper()
	s, _ := ownershiptest.Scan(w).Begin()
	read := s.Object(dependent.Ref)
	if read.State != ownership.Present {
		t.Fatalf("%v = %v", dependent.Ref, read.State)
	}
	var out []ownership.OwnerResolution
	for _, ref := range read.Object.OwnerReferences {
		out = append(out, ownership.ResolveOwner(s, read.Object, ref))
	}
	return out
}

// Each case is how Kubernetes' garbage collector judges the reference.
func TestResolveOwnerJudgesReferencesAsTheCollectorDoes(t *testing.T) {
	w := ownershiptest.Fixture()
	ns := func(group, kind, namespace, name string) resourceid.Ref {
		return resourceid.Ref{Group: group, Kind: kind, Namespace: namespace, Name: name}
	}
	for _, tc := range []struct {
		name      string
		dependent ownershiptest.Object
		want      []ownership.OwnerState
		wantRefs  []resourceid.Ref
	}{
		{"controller chain", object(t, w, "", "Pod", "alpha", "web-1-a"), []ownership.OwnerState{ownership.OwnerFound}, []resourceid.Ref{ns("apps", "ReplicaSet", "alpha", "web-1")}},
		{"two owners", object(t, w, "", "Pod", "alpha", "web-1-b"), []ownership.OwnerState{ownership.OwnerFound, ownership.OwnerFound}, []resourceid.Ref{ns("apps", "ReplicaSet", "alpha", "web-1"), ns("", "ConfigMap", "alpha", "cfg")}},
		{"an owner in another namespace is looked up in the dependent's", object(t, w, "", "Pod", "beta", "stray"), []ownership.OwnerState{ownership.OwnerGone}, []resourceid.Ref{ns("apps", "ReplicaSet", "beta", "web-1")}},
		{"the right UID under the wrong kind and name", object(t, w, "", "Pod", "alpha", "wrong-coordinates"), []ownership.OwnerState{ownership.OwnerGone}, []resourceid.Ref{ns("apps", "Deployment", "alpha", "nope")}},
		{"a later incarnation", object(t, w, "", "Pod", "alpha", "replaced"), []ownership.OwnerState{ownership.OwnerReplaced}, []resourceid.Ref{ns("apps", "ReplicaSet", "alpha", "web-old")}},
		{"no such owner", object(t, w, "", "Pod", "alpha", "dangling"), []ownership.OwnerState{ownership.OwnerGone}, []resourceid.Ref{ns("apps", "ReplicaSet", "alpha", "gone")}},
		{"a cluster-scoped owner of a namespaced object", object(t, w, "example.io", "Gadget", "beta", "g1"), []ownership.OwnerState{ownership.OwnerFound}, []resourceid.Ref{ns("", "Node", "", "n1")}},
		{"a cluster-scoped owner of a cluster-scoped object", object(t, w, "example.io", "ClusterWidget", "", "cw1"), []ownership.OwnerState{ownership.OwnerFound}, []resourceid.Ref{ns("", "Node", "", "n1")}},
		{"a namespaced owner of a cluster-scoped object", object(t, w, "", "Node", "", "n2"), []ownership.OwnerState{ownership.OwnerUnresolvable}, []resourceid.Ref{{}}},
		{"an owner version the API doesn't serve", object(t, w, "batch", "Job", "alpha", "j1"), []ownership.OwnerState{ownership.OwnerUnresolvable}, []resourceid.Ref{{}}},
		{"an owner at another served version", object(t, w, "", "ConfigMap", "alpha", "versioned"), []ownership.OwnerState{ownership.OwnerFound}, []resourceid.Ref{ns("example.io", "Widget", "alpha", "w1")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := resolve(t, w, tc.dependent)
			if len(got) != len(tc.want) {
				t.Fatalf("%d resolutions, want %d", len(got), len(tc.want))
			}
			for i := range got {
				if got[i].State != tc.want[i] || got[i].Ref != tc.wantRefs[i] {
					t.Errorf("reference %d = %v at %v, want %v at %v", i, got[i].State, got[i].Ref, tc.want[i], tc.wantRefs[i])
				}
			}
		})
	}
}

func TestResolveOwnerRefusesMalformedReferences(t *testing.T) {
	w := ownershiptest.Fixture()
	s, _ := ownershiptest.Scan(w).Begin()
	pod := s.Object(resourceid.Ref{Kind: "Pod", Namespace: "alpha", Name: "web-1-a"}).Object
	for _, ref := range []metav1.OwnerReference{
		{APIVersion: "apps/v1/extra", Kind: "ReplicaSet", Name: "web-1", UID: "uid-alpha-web-1"},
		{Kind: "ReplicaSet", Name: "web-1", UID: "uid-alpha-web-1"},
		{APIVersion: "apps/v1", Name: "web-1", UID: "uid-alpha-web-1"},
		{APIVersion: "apps/v1", Kind: "ReplicaSet", UID: "uid-alpha-web-1"},
		{APIVersion: "apps/v1", Kind: "Sprocket", Name: "x", UID: "uid-x"},
	} {
		if got := ownership.ResolveOwner(s, pod, ref); got.State != ownership.OwnerUnresolvable {
			t.Errorf("%+v = %v, want unresolvable", ref, got.State)
		}
	}
	if got := ownership.ResolveOwner(s, pod, metav1.OwnerReference{APIVersion: "v1", Kind: "Event", Name: "e1", UID: "uid-alpha-e1"}); got.State != ownership.OwnerExcluded {
		t.Errorf("an Event owner = %v, want excluded", got.State)
	}
}

// Without every owner's store, or without discovery's whole answer, the
// resolution is unknown, never gone or unresolvable.
func TestResolveOwnerIsUnknownWithoutTheEvidence(t *testing.T) {
	w := ownershiptest.Fixture()
	w.Collection[resourceid.GroupKind{Group: "apps", Kind: "ReplicaSet"}] = ownershiptest.Collection{State: ownershiptest.Loading}
	got := resolve(t, w, object(t, w, "", "Pod", "alpha", "dangling"))
	if got[0].State != ownership.OwnerUnknown || got[0].Gap.Reason != readreason.KindSyncPending {
		t.Fatalf("owner in a loading kind = %v %+v, want unknown kind_sync_pending", got[0].State, got[0].Gap)
	}

	w = ownershiptest.Fixture()
	w.Partial = true
	if got := resolve(t, w, object(t, w, "batch", "Job", "alpha", "j1")); got[0].State != ownership.OwnerUnknown || got[0].Gap.Reason != readreason.ReadError {
		t.Fatalf("unlisted owner version with partial discovery = %v %+v, want unknown read_error", got[0].State, got[0].Gap)
	}
	s, _ := ownershiptest.Scan(w).Begin()
	pod := s.Object(resourceid.Ref{Kind: "Pod", Namespace: "alpha", Name: "web-1-a"}).Object
	if got := ownership.ResolveOwner(s, pod, metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Sprocket", Name: "x", UID: "uid-x"}); got.State != ownership.OwnerUnknown {
		t.Fatalf("unlisted owner kind with partial discovery = %v, want unknown", got.State)
	}
}

// An Event owner is excluded, as an Event read is, even when partial
// discovery didn't list Events.
func TestResolveOwnerExcludesEventOwnersWhateverDiscoveryLists(t *testing.T) {
	w := ownershiptest.Fixture()
	w.Partial = true
	var resources []ownership.ServedResource
	for _, r := range w.Resources {
		if r.Kind != "Event" {
			resources = append(resources, r)
		}
	}
	w.Resources = resources
	s, _ := ownershiptest.Scan(w).Begin()
	pod := s.Object(resourceid.Ref{Kind: "Pod", Namespace: "alpha", Name: "web-1-a"}).Object
	for _, apiVersion := range []string{"v1", "events.k8s.io/v1"} {
		if got := ownership.ResolveOwner(s, pod, metav1.OwnerReference{APIVersion: apiVersion, Kind: "Event", Name: "e1", UID: "uid-alpha-e1"}); got.State != ownership.OwnerExcluded {
			t.Errorf("an Event owner at %s, Events unlisted = %v %+v, want excluded", apiVersion, got.State, got.Gap)
		}
	}
}
