package cachesource

import (
	"cmp"
	"fmt"
	"reflect"
	"slices"
	"testing"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// conform checks the cache source over w against Scan of w on every question
// QueriesFor asks.
func conform(t *testing.T, w ownershiptest.World) {
	t.Helper()
	want, err := ownershiptest.Scan(w).Begin()
	if err != nil {
		t.Fatal(err)
	}
	r := render(t, w)
	before := r.storedObjects()
	got := begin(t, r.source())
	ownershiptest.Conform(t, want, got, ownershiptest.QueriesFor(w))
	if err := got.Validate(); err != nil {
		t.Fatalf("a quiet cache's session failed Validate: %v", err)
	}
	resolveAlike(t, w, want, got)
	if after := r.storedObjects(); !reflect.DeepEqual(before, after) {
		t.Fatal("answering changed objects in the cache")
	}
}

// resolveAlike checks that ResolveOwner judges every owner reference of every
// object in w the same over both sessions: the cache source changes nothing a
// rule can see.
func resolveAlike(t *testing.T, w ownershiptest.World, want, got ownership.Session) {
	t.Helper()
	for _, o := range w.Objects {
		dependent := want.Object(o.Ref)
		if dependent.State != ownership.Present {
			continue
		}
		for i, ref := range dependent.Object.OwnerReferences {
			a, b := ownership.ResolveOwner(want, dependent.Object, ref), ownership.ResolveOwner(got, dependent.Object, ref)
			if a.State != b.State || a.Ref != b.Ref || a.Gap != b.Gap || a.Owner.UID != b.Owner.UID || a.Owner.ResourceVersion != b.Owner.ResourceVersion {
				t.Errorf("%v owner %d (%s %s/%s): scan %v %v %+v, cache %v %v %+v", o.Ref, i, ref.APIVersion, ref.Kind, ref.Name, a.State, a.Ref, a.Gap, b.State, b.Ref, b.Gap)
			}
		}
	}
}

// storedObjects deep-copies every object Radar's stores hold, by kind.
func (r *rendered) storedObjects() map[string][]runtime.Object {
	out := map[string][]runtime.Object{}
	for _, k := range ownershiptest.Kinds {
		var objs []runtime.Object
		if k.Typed {
			held, _ := r.caches.Typed.ReadableObjects(k.Resource, nil)
			for _, o := range held {
				objs = append(objs, o.DeepCopyObject())
			}
		} else {
			held, _ := r.caches.Dynamic.ListWatchedReadOnly(schema.GroupVersionResource{Group: k.Group, Version: k.Version, Resource: k.Resource})
			for _, o := range held {
				objs = append(objs, o.DeepCopyObject())
			}
		}
		slices.SortFunc(objs, func(a, b runtime.Object) int {
			am, _ := apimeta.Accessor(a)
			bm, _ := apimeta.Accessor(b)
			return cmp.Compare(am.GetNamespace()+"/"+am.GetName(), bm.GetNamespace()+"/"+bm.GetName())
		})
		out[k.GroupKind.String()] = objs
	}
	return out
}

var (
	pods, replicaSets, configMaps, jobs    = resourceid.GroupKind{Kind: "Pod"}, resourceid.GroupKind{Group: "apps", Kind: "ReplicaSet"}, resourceid.GroupKind{Kind: "ConfigMap"}, resourceid.GroupKind{Group: "batch", Kind: "Job"}
	widgetKind, gadgetKind, clusterWidgets = resourceid.GroupKind{Group: "example.io", Kind: "Widget"}, resourceid.GroupKind{Group: "example.io", Kind: "Gadget"}, resourceid.GroupKind{Group: "example.io", Kind: "ClusterWidget"}
)

func TestSourceConformsOnTheFixture(t *testing.T) {
	for name, collect := range map[string]map[resourceid.GroupKind]ownershiptest.Collection{
		"everything collected": nil,
		"some namespaces collected": {
			pods:       {Namespaces: []string{"alpha"}},
			widgetKind: {Namespaces: []string{"alpha"}, OnDemand: true},
			gadgetKind: {Namespaces: []string{"alpha", "beta"}, OnDemand: true},
		},
		"still loading": {
			replicaSets: {State: ownershiptest.Loading},
			gadgetKind:  {State: ownershiptest.Loading, OnDemand: true},
			pods:        {State: ownershiptest.Loading, Namespaces: []string{"alpha", "beta"}},
			widgetKind:  {State: ownershiptest.Loading, Namespaces: []string{"alpha"}, OnDemand: true},
		},
		"not collected or refused": {
			configMaps:     {State: ownershiptest.NotCollected},
			jobs:           {State: ownershiptest.Refused},
			widgetKind:     {State: ownershiptest.NotCollected, OnDemand: true},
			clusterWidgets: {State: ownershiptest.Refused, OnDemand: true},
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := ownershiptest.Fixture()
			for gk, c := range collect {
				w.Collection[gk] = c
			}
			conform(t, w)
		})
	}
	t.Run("partial discovery", func(t *testing.T) {
		w := ownershiptest.Fixture()
		w.Partial = true
		conform(t, w)
	})
}

func TestSourceConformsOnGeneratedWorlds(t *testing.T) {
	seeds := 40
	if testing.Short() {
		seeds = 8
	}
	for seed := range uint64(seeds) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			conform(t, ownershiptest.Generate(seed))
		})
	}
}
