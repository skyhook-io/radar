package ownership_test

import (
	"context"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func TestDeletionSharedOwnersAndUnknowns(t *testing.T) {
	root := ownershiptest.Object{Ref: at("", "ConfigMap", "alpha", "root"), APIVersion: "v1", UID: "root"}
	left := ownershiptest.Object{Ref: at("", "ConfigMap", "alpha", "left"), APIVersion: "v1", UID: "left", OwnerReferences: []metav1.OwnerReference{root.OwnerRef(false)}}
	right := ownershiptest.Object{Ref: at("", "ConfigMap", "alpha", "right"), APIVersion: "v1", UID: "right", OwnerReferences: []metav1.OwnerReference{root.OwnerRef(false)}}
	shared := ownershiptest.Object{Ref: at("", "ConfigMap", "alpha", "shared"), APIVersion: "v1", UID: "shared", OwnerReferences: []metav1.OwnerReference{left.OwnerRef(false), right.OwnerRef(false)}}
	leaf := ownershiptest.Object{Ref: at("", "ConfigMap", "alpha", "leaf"), APIVersion: "v1", UID: "leaf", OwnerReferences: []metav1.OwnerReference{shared.OwnerRef(false)}}
	external := ownershiptest.Object{Ref: at("", "Secret", "alpha", "external"), APIVersion: "v1", UID: "external"}
	for _, tc := range []struct {
		name                      string
		external, denied, missing bool
		certain, possible         int
	}{
		{"both owners collected", false, false, false, 4, 0},
		{"live owner preserves descendants", true, false, false, 2, 0},
		{"withheld owner leaves uncertainty", true, true, false, 2, 2},
		{"missing owner does not preserve descendants", true, false, true, 4, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := ownershiptest.World{Resources: []ownership.ServedResource{
				{Version: "v1", Kind: "ConfigMap", Resource: "configmaps", Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}},
				{Version: "v1", Kind: "Secret", Resource: "secrets", Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}},
			}, Objects: []ownershiptest.Object{root, left, right, shared, leaf}}
			if tc.external {
				w.Objects[3].OwnerReferences = append(w.Objects[3].OwnerReferences, external.OwnerRef(false))
				if !tc.missing {
					w.Objects = append(w.Objects, external)
				}
			}
			var a ownership.Access = ownership.Unrestricted()
			if tc.denied {
				a = access{denied: []resourceid.GroupKind{{Kind: "Secret"}}}
			}
			got := view(t, w, a).Deletion(context.Background(), root.Ref, 100)
			if len(got.Dependents) != tc.certain || len(got.Possible) != tc.possible {
				t.Fatalf("got certain %v possible %v", got.Dependents, got.Possible)
			}
		})
	}
}

func TestDeletionBoundAndCancellation(t *testing.T) {
	w := ownershiptest.Fixture()
	r := at("apps", "Deployment", "alpha", "web")
	got := view(t, w, ownership.Unrestricted()).Deletion(context.Background(), r, 1)
	if !got.Truncated || len(got.Dependents) != 0 {
		t.Fatalf("bounded = %+v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got = view(t, w, ownership.Unrestricted()).Deletion(ctx, r, 100)
	if !got.Truncated || len(got.Dependents) != 0 {
		t.Fatalf("canceled = %+v", got)
	}
}
