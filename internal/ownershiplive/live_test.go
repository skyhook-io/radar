//go:build liveparity

package ownershiplive

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/cachesource"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// typedKinds are the kinds Radar's typed cache holds.
var typedKinds = func() map[resourceid.GroupKind]resourceid.Builtin {
	out := map[resourceid.GroupKind]resourceid.Builtin{}
	for _, kl := range k8score.AllKindListers() {
		if b, ok := resourceid.BuiltinForKind(kl.Kind()); ok && b.Group == kl.Group() {
			out[b.GroupKind()] = b
		}
	}
	return out
}()

// passes is how many times the comparison runs on a fresh list. Objects
// changing between the cache's view and a list make a question differ in one
// pass; a defect makes it differ in every pass.
const passes = 3

// TestLiveParity checks the cache source over a live cluster against Scan of
// a World listed straight from the same cluster, and fails on any question
// the two answer differently in every one of several passes. It only lists
// and watches.
//
//	RADAR_LIVE_KUBECONFIG=... RADAR_LIVE_CONTEXT=... go test -tags liveparity,withoutebpf -run TestLiveParity -timeout 20m ./internal/ownershiplive/
func TestLiveParity(t *testing.T) {
	kubeconfig, kubeContext := os.Getenv("RADAR_LIVE_KUBECONFIG"), os.Getenv("RADAR_LIVE_CONTEXT")
	if kubeconfig == "" || kubeContext == "" {
		t.Skip("set RADAR_LIVE_KUBECONFIG and RADAR_LIVE_CONTEXT")
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig},
		&clientcmd.ConfigOverrides{CurrentContext: kubeContext},
	).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.QPS, cfg.Burst = 50, 100
	clients := kubernetes.NewForConfigOrDie(cfg)
	dyn := dynamic.NewForConfigOrDie(cfg)
	discovery, err := k8score.NewResourceDiscovery(clients.Discovery())
	if err != nil {
		t.Fatal(err)
	}
	catalog := discovery.Catalog()

	enabled := map[string]bool{}
	for gk, b := range typedKinds {
		if gk.Kind != "Secret" && gk.Kind != "Event" {
			enabled[b.Resource] = true
		}
	}
	start := time.Now()
	typedCache, err := k8score.NewResourceCache(k8score.CacheConfig{Client: clients, ResourceTypes: enabled})
	if err != nil {
		t.Fatal(err)
	}
	defer typedCache.Stop()
	dynamicCache, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{DynamicClient: dyn, Discovery: discovery})
	if err != nil {
		t.Fatal(err)
	}
	defer dynamicCache.Stop()

	// Watch every collectible custom kind at its most stable served version.
	served := map[schema.GroupResource]k8score.APIResource{}
	for _, r := range catalog.Resources {
		gr := schema.GroupResource{Group: r.Group, Resource: r.Name}
		if prev, ok := served[gr]; !ok || k8score.IsMoreStableVersion(r.Version, prev.Version) {
			served[gr] = r
		}
	}
	collectible := func(r k8score.APIResource) bool {
		return slices.Contains(r.Verbs, "list") && slices.Contains(r.Verbs, "watch") && slices.Contains(r.Verbs, "delete")
	}
	type collected struct {
		gvr      schema.GroupVersionResource
		gk       resourceid.GroupKind
		onDemand bool
	}
	var kinds []collected
	for gk, b := range typedKinds {
		if enabled[b.Resource] {
			kinds = append(kinds, collected{gvr: schema.GroupVersionResource{Group: b.Group, Version: b.Version, Resource: b.Resource}, gk: gk})
		}
	}
	for _, r := range served {
		gk := resourceid.GroupKind{Group: r.Group, Kind: r.Kind}
		if _, isTyped := typedKinds[gk]; isTyped || ownership.IsEvent(gk) || !collectible(r) {
			continue
		}
		gvr := schema.GroupVersionResource{Group: r.Group, Version: r.Version, Resource: r.Name}
		if err := dynamicCache.EnsureWatching(gvr); err != nil {
			t.Logf("not watching %v: %v", gvr, err)
			continue
		}
		kinds = append(kinds, collected{gvr: gvr, gk: gk, onDemand: true})
	}
	for _, k := range kinds {
		if k.onDemand && !dynamicCache.WaitForSync(k.gvr, 60*time.Second) {
			t.Fatalf("%v did not sync", k.gvr)
		}
	}
	t.Logf("caches loaded in %v: %d kinds", time.Since(start).Round(time.Millisecond), len(kinds))

	// world lists the cluster afresh: discovery as served, every object of
	// every collected kind, each kind collected as the caches collect it.
	world := func() ownershiptest.World {
		w := ownershiptest.World{Partial: !catalog.Complete, Collection: map[resourceid.GroupKind]ownershiptest.Collection{}}
		for _, r := range catalog.Resources {
			w.Resources = append(w.Resources, ownership.ServedResource{Group: r.Group, Version: r.Version, Kind: r.Kind, Resource: r.Name, Namespaced: r.Namespaced, Verbs: r.Verbs})
			gk := resourceid.GroupKind{Group: r.Group, Kind: r.Kind}
			_, isTyped := typedKinds[gk]
			c := ownershiptest.Collection{State: ownershiptest.NotCollected, OnDemand: !isTyped}
			if !slices.Contains(r.Verbs, "watch") || !slices.Contains(r.Verbs, "list") {
				c.State = ownershiptest.Unwatchable
			}
			if prev, ok := w.Collection[gk]; !ok || prev.State == ownershiptest.Unwatchable {
				w.Collection[gk] = c
			}
		}
		for _, k := range kinds {
			items, err := dyn.Resource(k.gvr).List(context.Background(), metav1.ListOptions{})
			if err != nil {
				t.Fatalf("list %v: %v", k.gvr, err)
			}
			w.Collection[k.gk] = ownershiptest.Collection{OnDemand: k.onDemand}
			for _, u := range items.Items {
				w.Objects = append(w.Objects, ownershiptest.Object{
					Ref:        resourceid.Ref{Group: k.gk.Group, Kind: k.gk.Kind, Namespace: u.GetNamespace(), Name: u.GetName()},
					APIVersion: k.gvr.GroupVersion().String(), UID: u.GetUID(), ResourceVersion: u.GetResourceVersion(),
					Owners: u.GetOwnerReferences(), DeletionTimestamp: u.GetDeletionTimestamp(), Finalizers: u.GetFinalizers(),
				})
			}
		}
		return w
	}

	// questions asks about every object and a missing one per kind, every
	// UID an object has or names plus one nothing has, and domains around a
	// sample of namespaces.
	questions := func(w ownershiptest.World) ownershiptest.Queries {
		q := ownershiptest.Queries{Domains: []ownership.Domain{ownership.Everywhere(), {Namespaces: []string{""}, ClusterScoped: true}}, Limits: []int{0}}
		uids := map[types.UID]bool{"uid-nothing-has": true}
		missing := map[resourceid.GroupKind]bool{}
		var namespaces []string
		for _, o := range w.Objects {
			q.Refs = append(q.Refs, o.Ref)
			if !missing[o.Ref.GroupKind()] {
				missing[o.Ref.GroupKind()] = true
				absent := o.Ref
				absent.Name += "-not-there"
				q.Refs = append(q.Refs, absent)
			}
			uids[o.UID] = true
			for _, ref := range o.Owners {
				uids[ref.UID] = true
			}
			if o.Ref.Namespace != "" && !slices.Contains(namespaces, o.Ref.Namespace) {
				namespaces = append(namespaces, o.Ref.Namespace)
			}
		}
		for uid := range uids {
			q.UIDs = append(q.UIDs, uid)
		}
		slices.Sort(q.UIDs)
		slices.Sort(namespaces)
		for i, ns := range namespaces {
			if i%5 == 0 {
				q.Domains = append(q.Domains, ownership.InNamespace(ns))
			}
		}
		return q
	}

	persistent := map[string]int{}
	var last []ownershiptest.Difference
	for pass := range passes {
		w := world()
		want, _ := ownershiptest.Scan(w).Begin()
		got, err := cachesource.New(func() cachesource.Caches {
			return cachesource.Caches{Typed: typedCache, Dynamic: dynamicCache, Discovery: discovery}
		}).Begin()
		if err != nil {
			t.Fatal(err)
		}
		q := questions(w)
		diffs := ownershiptest.Differences(want, got, q)
		for _, o := range w.Objects {
			dependent := want.Object(o.Ref)
			if dependent.State != ownership.Present {
				continue
			}
			for i, ref := range dependent.Object.OwnerReferences {
				a, b := ownership.ResolveOwner(want, dependent.Object, ref), ownership.ResolveOwner(got, dependent.Object, ref)
				if a.State != b.State || a.Ref != b.Ref || a.Gap != b.Gap {
					diffs = append(diffs, ownershiptest.Difference{
						Question: fmt.Sprintf("ResolveOwner(%v, %d)", o.Ref, i),
						Want:     fmt.Sprintf("%v %v %+v", a.State, a.Ref, a.Gap), Got: fmt.Sprintf("%v %v %+v", b.State, b.Ref, b.Gap),
					})
				}
			}
		}
		if err := got.Validate(); err != nil {
			t.Errorf("pass %d: Validate: %v", pass, err)
		}
		coverage := 0
		for _, d := range diffs {
			persistent[d.Question]++
			if d.Coverage {
				coverage++
			}
		}
		t.Logf("pass %d: %d objects, %d questions (%d objects, %d UIDs × %d domains): %d differ (%d about coverage)",
			pass, len(w.Objects), len(q.Refs)+len(q.UIDs)*len(q.Domains), len(q.Refs), len(q.UIDs), len(q.Domains), len(diffs), coverage)
		last = diffs
		if pass < passes-1 {
			time.Sleep(5 * time.Second)
		}
	}
	failures := 0
	for _, d := range last {
		if persistent[d.Question] == passes {
			failures++
			if failures <= 20 {
				t.Errorf("differs in every pass: %v", d)
			}
		}
	}
	t.Logf("%d questions differed in every pass", failures)
}
