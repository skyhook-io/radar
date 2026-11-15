package cachesource

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"

	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/pkg/configrefs"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// declaringKinds are the World's kinds whose pod spec or template declares.
var declaringKinds = []resourceid.GroupKind{
	{Kind: "Pod"}, {Group: "apps", Kind: "ReplicaSet"}, {Group: "apps", Kind: "Deployment"}, {Group: "batch", Kind: "Job"},
}

// withDeclarations gives the declaring objects of w references to
// ConfigMaps in their namespace, present or not. ConfigMaps are the only
// declarable kind a generated World serves; the others are literal cases.
func withDeclarations(seed uint64, w ownershiptest.World) (ownershiptest.World, []resourceid.Ref) {
	r := rand.New(rand.NewPCG(seed, seed^0x5bd1e995))
	var targets []resourceid.Ref
	for _, o := range w.Objects {
		if o.Ref.Kind == "ConfigMap" && o.Ref.Group == "" {
			targets = append(targets, o.Ref)
		}
	}
	for _, ns := range []string{"alpha", "beta", "gamma"} {
		targets = append(targets, resourceid.Ref{Kind: "ConfigMap", Namespace: ns, Name: "missing"})
	}
	for i := range w.Objects {
		o := &w.Objects[i]
		if !slices.Contains(declaringKinds, o.Ref.GroupKind()) || r.IntN(4) == 0 {
			continue
		}
		var local []resourceid.Ref
		for _, t := range targets {
			if t.Namespace == o.Ref.Namespace {
				local = append(local, t)
			}
		}
		for range 1 + r.IntN(3) {
			pick := local[r.IntN(len(local))]
			if !slices.Contains(o.Declares, pick) {
				o.Declares = append(o.Declares, pick)
			}
		}
	}
	return w, targets
}

// consumerKinds are every kind ConfigConsumers asks the session for.
var consumerKinds = []resourceid.GroupKind{
	{Group: "apps", Kind: "Deployment"}, {Group: configrefs.ArgoRolloutsGroup, Kind: "Rollout"}, {Group: "apps", Kind: "StatefulSet"},
	{Group: "apps", Kind: "DaemonSet"}, {Group: "batch", Kind: "CronJob"}, {Group: "batch", Kind: "Job"},
	{Group: "apps", Kind: "ReplicaSet"}, {Group: "networking.k8s.io", Kind: "Ingress"}, {Kind: "Pod"},
}

func indexedRefs(r relations.IndexedRead) []resourceid.Ref {
	refs := make([]resourceid.Ref, 0, len(r.Objects))
	for _, o := range r.Objects {
		refs = append(refs, o.Ref)
	}
	return refs
}

// exercised counts the answers a conformance run compared that said
// something, so the generated worlds can't pass by answering nothing.
type exercised struct{ consumers, gaps, narrowed int }

// conformRelations checks the cache source's index reads and the relations
// View over it against Scan of w, for every target and object of w.
func conformRelations(t *testing.T, w ownershiptest.World, targets []resourceid.Ref, seen *exercised) {
	t.Helper()
	scanned, err := ownershiptest.Scan(w).Begin()
	if err != nil {
		t.Fatal(err)
	}
	want := scanned.(relations.Session)
	got := begin(t, render(t, w).source()).(relations.Session)
	for _, target := range targets {
		for _, gk := range consumerKinds {
			a := want.Indexed(gk, cache.NamespaceIndex, []string{target.Namespace}, ownership.InNamespace(target.Namespace))
			b := got.Indexed(gk, cache.NamespaceIndex, []string{target.Namespace}, ownership.InNamespace(target.Namespace))
			if !slices.Equal(indexedRefs(a), indexedRefs(b)) || !slices.Equal(a.Gaps, b.Gaps) {
				t.Errorf("Indexed(%v, %v): scan %v %v, cache %v %v", gk, target, indexedRefs(a), a.Gaps, indexedRefs(b), b.Gaps)
			}
		}
	}
	for _, access := range []ownership.Access{ownership.Unrestricted(), alphaOnly{}} {
		vw, vg := relations.NewView(want, access), relations.NewView(got, access)
		for _, target := range targets {
			a, b := vw.ConfigConsumers(target), vg.ConfigConsumers(target)
			if !reflect.DeepEqual(a, b) {
				t.Errorf("%T ConfigConsumers(%v):\nscan  %+v\ncache %+v", access, target, a, b)
			}
			if len(b.Consumers) > 0 {
				seen.consumers++
			}
			if len(b.Coverage.NotChecked) > 0 {
				seen.gaps++
			}
			if b.Coverage.Narrowed {
				seen.narrowed++
			}
		}
		for _, o := range w.Objects {
			if a, b := vw.ConfigReferences(o.Ref, nil), vg.ConfigReferences(o.Ref, nil); !reflect.DeepEqual(a, b) {
				t.Errorf("%T ConfigReferences(%v):\nscan  %+v\ncache %+v", access, o.Ref, a, b)
			}
		}
	}
}

func TestRelationsConformOnGeneratedWorlds(t *testing.T) {
	seeds := 24
	if testing.Short() {
		seeds = 6
	}
	var seen exercised
	for seed := range uint64(seeds) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			w, targets := withDeclarations(seed, ownershiptest.Generate(seed))
			conformRelations(t, w, targets, &seen)
		})
	}
	if seen.consumers == 0 || seen.gaps == 0 || seen.narrowed == 0 {
		t.Fatalf("the generated worlds didn't exercise every answer: %+v", seen)
	}
}
