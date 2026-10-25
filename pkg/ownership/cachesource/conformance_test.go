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
	if err := got.ValidateCaches(); err != nil {
		t.Fatalf("a quiet cache's session failed ValidateCaches: %v", err)
	}
	resolveAlike(t, w, want, got)
	viewsAlike(t, w, want, got)
	if after := r.storedObjects(); !reflect.DeepEqual(before, after) {
		t.Fatal("answering changed objects in the cache")
	}
}

// resolveAlike checks that ResolveOwner judges every owner reference of every
// object in w the same over both sessions: the cache source changes nothing a
// rule can see.
func resolveAlike(t *testing.T, w ownershiptest.World, want, got ownership.ReadSession) {
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

// viewsAlike checks that a View gives every answer about every object in w
// alike over both sessions, for a caller who may read everything and for one
// who may read less.
func viewsAlike(t *testing.T, w ownershiptest.World, want, got ownership.ReadSession) {
	t.Helper()
	for _, caller := range []ownership.Access{ownership.Unrestricted(), alphaOnly{}} {
		a, b := ownership.NewView(want, caller), ownership.NewView(got, caller)
		for _, o := range w.Objects {
			if x, y := fmt.Sprint(viewAnswers(a, o.Ref)), fmt.Sprint(viewAnswers(b, o.Ref)); x != y {
				t.Errorf("%v for %T:\n scan  %s\n cache %s", o.Ref, caller, x, y)
			}
		}
	}
}

// viewAnswers is what a View says about ref, without the resource versions
// and copies two sources may hold differently.
func viewAnswers(v *ownership.View, ref resourceid.Ref) []any {
	ident := func(o ownership.Observation) string { return fmt.Sprintf("%v@%s", o.Ref, o.UID) }
	resolution := func(r ownership.OwnerResolution) string {
		return fmt.Sprintf("%v %v %s %+v", r.State, r.Ref, r.Owner.UID, r.Gap)
	}
	subject, refs := v.References(ref)
	out := []any{subject.Access, subject.Read.State, subject.Read.Gap, subject.Read.Object.UID}
	for _, r := range refs {
		out = append(out, resolution(r))
	}
	owner := v.Owner(ref)
	out = append(out, owner.Reference != nil, owner.Several, resolution(owner.Resolution))
	if r := owner.Reference; r != nil {
		out = append(out, r.APIVersion, r.Kind, r.Name, r.UID, r.Controller != nil && *r.Controller)
	}
	children := v.Children(ref)
	for _, c := range children.Children {
		out = append(out, ident(c))
	}
	out = append(out, children.Gaps, children.Narrowed)
	root := v.ControllerChain(ref)
	out = append(out, root.Chain, root.Cycle, root.Truncated)
	if root.UnfollowedReference != nil {
		out = append(out, resolution(*root.UnfollowedReference))
	}
	if ownership.IsRecordingManager(ref.GroupKind()) {
		members := v.Members(ref)
		for _, m := range members.Members {
			out = append(out, ident(m))
		}
		out = append(out, members.Membership, members.Missing, members.Withheld, members.Unknown, members.Gaps)
	}
	managers := v.RecordingManagers(ref)
	for _, m := range managers.Managers {
		out = append(out, ident(m))
	}
	out = append(out, managers.Unreadable, managers.Remote, managers.Gaps)
	return out
}

// alphaOnly may read namespaced objects only in alpha, and no Nodes.
type alphaOnly struct{}

func (alphaOnly) CanRead(gk resourceid.GroupKind, namespace string) ownership.Decision {
	if gk == (resourceid.GroupKind{Kind: "Node"}) || namespace != "" && namespace != "alpha" {
		return ownership.Denied
	}
	return ownership.Allowed
}
func (alphaOnly) Namespaces() ([]string, bool) { return []string{"alpha"}, false }

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
		"managers partly collected": {
			ownership.ArgoApplication:   {State: ownershiptest.Loading, OnDemand: true},
			ownership.FluxKustomization: {Namespaces: []string{"alpha"}, OnDemand: true},
			ownership.FluxHelmRelease:   {State: ownershiptest.Refused, OnDemand: true},
		},
		"managers not collected": {
			ownership.ArgoApplication:   {State: ownershiptest.NotCollected, OnDemand: true},
			ownership.FluxKustomization: {Namespaces: []string{"alpha", "flux-system"}, OnDemand: true},
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
	t.Run("ambiguous kinds", func(t *testing.T) {
		w := ownershiptest.Fixture()
		gc := []string{"delete", "get", "list", "watch"}
		w.Resources = append(w.Resources,
			// Collected through either of two namespaced resources.
			ownership.ServedResource{Group: "example.io", Version: "v1", Kind: "Doohickey", Resource: "doohickeys", Namespaced: true, Verbs: gc},
			ownership.ServedResource{Group: "example.io", Version: "v1", Kind: "Doohickey", Resource: "moredoohickeys", Namespaced: true, Verbs: gc},
			// Collected only through its cluster-scoped resource.
			ownership.ServedResource{Group: "example.io", Version: "v1", Kind: "Whatsit", Resource: "whatsits", Namespaced: true, Verbs: []string{"get"}},
			ownership.ServedResource{Group: "example.io", Version: "v1", Kind: "Whatsit", Resource: "clusterwhatsits", Verbs: gc},
			// Served only to answer queries, as aggregated APIs do.
			ownership.ServedResource{Group: "example.io", Version: "v1", Kind: "Report", Resource: "reports", Namespaced: true},
			ownership.ServedResource{Group: "example.io", Version: "v1", Kind: "Report", Resource: "clusterreports"},
		)
		conform(t, w)
	})
	// A manager kind discovery doesn't serve is not installed, unless
	// discovery missed some groups.
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprintf("HelmReleases not served, partial discovery %v", partial), func(t *testing.T) {
			w := ownershiptest.Fixture()
			w.Partial = partial
			w.Resources = slices.DeleteFunc(w.Resources, func(r ownership.ServedResource) bool { return r.Kind == ownership.FluxHelmRelease.Kind })
			w.Objects = slices.DeleteFunc(w.Objects, func(o ownershiptest.Object) bool { return o.Ref.GroupKind() == ownership.FluxHelmRelease })
			w.Collection[ownership.FluxHelmRelease] = ownershiptest.Collection{State: ownershiptest.NotCollected, OnDemand: true}
			conform(t, w)
		})
	}
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
