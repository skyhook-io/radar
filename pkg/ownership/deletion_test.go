package ownership_test

import (
	"context"
	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func BenchmarkDeletionLongChain(b *testing.B) {
	fixture := ownershiptest.NewBuilder()
	root := fixture.Add("", "ConfigMap", "chain", "0")
	owner := root
	for i := 1; i < 1000; i++ {
		owner = fixture.Add("", "ConfigMap", "chain", fmt.Sprint(i), owner.OwnerRef(false))
	}
	session, err := ownershiptest.Scan(fixture.World()).Begin()
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		got := ownership.NewView(session, ownership.Unrestricted()).Deletion(context.Background(), root.Ref, 1000)
		if got.Truncated || len(got.Dependents) != 999 || len(got.Possible) != 0 {
			b.Fatalf("chain returned %d dependents, %d possible, truncated %v", len(got.Dependents), len(got.Possible), got.Truncated)
		}
	}
}

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

func TestDeletionCyclesInvalidReferencesAndUnvisitedCoOwner(t *testing.T) {
	for _, mode := range []string{"cap", "cycle", "unserved", "unserved reference version", "different owner kind", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			r := ownershiptest.Object{Ref: at("", "ConfigMap", "alpha", "root"), APIVersion: "v1", UID: "root"}
			a := ownershiptest.Object{Ref: at("", "ConfigMap", "alpha", "a"), APIVersion: "v1", UID: "a", OwnerReferences: []metav1.OwnerReference{r.OwnerRef(false)}}
			b := ownershiptest.Object{Ref: at("", "ConfigMap", "alpha", "z"), APIVersion: "v1", UID: "b", OwnerReferences: []metav1.OwnerReference{a.OwnerRef(false)}}
			c := ownershiptest.Object{Ref: at("", "ConfigMap", "alpha", "c"), APIVersion: "v1", UID: "c", OwnerReferences: []metav1.OwnerReference{a.OwnerRef(false), b.OwnerRef(false)}}
			limit, certain, possible := 100, 0, 3
			switch mode {
			case "cap":
				limit, certain, possible = 3, 1, 1
			case "cycle":
				a.OwnerReferences = append(a.OwnerReferences, b.OwnerRef(false))
			case "unserved reference version":
				a.OwnerReferences[0].APIVersion = "v99"
			case "different owner kind":
				a.OwnerReferences[0].Kind = "UndiscoveredKind"
				possible = 0
			case "unserved":
				a.OwnerReferences = append(a.OwnerReferences, metav1.OwnerReference{APIVersion: "missing.io/v1", Kind: "Ghost", Name: "ghost", UID: "ghost"})
			case "invalid":
				a.OwnerReferences = append(a.OwnerReferences, metav1.OwnerReference{APIVersion: "bad//v1", Kind: "Ghost", Name: "ghost", UID: "ghost"})
				possible = 0
			}
			w := ownershiptest.World{Resources: []ownership.ServedResource{{Version: "v1", Kind: "ConfigMap", Resource: "configmaps", Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}}}, Objects: []ownershiptest.Object{r, a, b, c}}
			got := view(t, w, ownership.Unrestricted()).Deletion(context.Background(), r.Ref, limit)
			if len(got.Dependents) != certain || len(got.Possible) != possible || got.Truncated != (mode == "cap") {
				t.Fatalf("projection %+v", got)
			}
		})
	}
}
