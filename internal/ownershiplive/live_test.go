//go:build liveparity

package ownershiplive

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

// TestLiveParity checks the cache source over a live cluster against Scan of
// a World listed straight from the same cluster. It only lists and watches.
//
//	RADAR_LIVE_KUBECONFIG=... RADAR_LIVE_CONTEXT=... go test -tags liveparity,withoutebpf -run TestLiveParity ./internal/ownershiplive/
//
// Objects that changed between the cache's view and the list differ for that
// reason alone; the test reports them apart and fails on any other difference.
func TestLiveParity(t *testing.T) {
	kubeconfig, context_ := os.Getenv("RADAR_LIVE_KUBECONFIG"), os.Getenv("RADAR_LIVE_CONTEXT")
	if kubeconfig == "" || context_ == "" {
		t.Skip("set RADAR_LIVE_KUBECONFIG and RADAR_LIVE_CONTEXT")
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig},
		&clientcmd.ConfigOverrides{CurrentContext: context_},
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

	typed := map[string]resourceid.Builtin{}
	enabled := map[string]bool{}
	for gk, b := range typedKinds {
		if gk.Kind == "Secret" || gk.Kind == "Event" {
			continue
		}
		typed[b.Resource] = b
		enabled[b.Resource] = true
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
	var watched []schema.GroupVersionResource
	for _, r := range served {
		gk := resourceid.GroupKind{Group: r.Group, Kind: r.Kind}
		if _, isTyped := typedKinds[gk]; isTyped || ownership.IsEvent(gk) || !slices.Contains(r.Verbs, "watch") || !slices.Contains(r.Verbs, "list") || !slices.Contains(r.Verbs, "delete") {
			continue
		}
		gvr := schema.GroupVersionResource{Group: r.Group, Version: r.Version, Resource: r.Name}
		if err := dynamicCache.EnsureWatching(gvr); err != nil {
			t.Logf("not watching %v: %v", gvr, err)
			continue
		}
		watched = append(watched, gvr)
	}
	for _, gvr := range watched {
		if !dynamicCache.WaitForSync(gvr, 60*time.Second) {
			t.Fatalf("%v did not sync", gvr)
		}
	}
	t.Logf("caches loaded in %v: %d typed kinds, %d custom kinds", time.Since(start).Round(time.Millisecond), len(enabled), len(watched))

	// The World: discovery as served, every object listed afresh, and each
	// kind collected as the caches above collect it.
	w := ownershiptest.World{Partial: !catalog.Complete, Collection: map[resourceid.GroupKind]ownershiptest.Collection{}}
	for _, r := range catalog.Resources {
		w.Resources = append(w.Resources, ownership.ServedResource{Group: r.Group, Version: r.Version, Kind: r.Kind, Resource: r.Name, Namespaced: r.Namespaced, Verbs: r.Verbs})
		gk := resourceid.GroupKind{Group: r.Group, Kind: r.Kind}
		_, isTyped := typedKinds[gk]
		c := ownershiptest.Collection{State: ownershiptest.NotCollected, OnDemand: !isTyped}
		if !slices.Contains(r.Verbs, "watch") || !slices.Contains(r.Verbs, "list") {
			c.State = ownershiptest.Unwatchable
		}
		if _, ok := w.Collection[gk]; !ok || c.State == ownershiptest.NotCollected {
			w.Collection[gk] = c
		}
	}
	list := func(gvr schema.GroupVersionResource, gk resourceid.GroupKind, onDemand bool) {
		items, err := dyn.Resource(gvr).List(context.Background(), metav1.ListOptions{})
		if err != nil {
			t.Fatalf("list %v: %v", gvr, err)
		}
		w.Collection[gk] = ownershiptest.Collection{OnDemand: onDemand}
		for _, u := range items.Items {
			w.Objects = append(w.Objects, ownershiptest.Object{
				Ref:        resourceid.Ref{Group: gk.Group, Kind: gk.Kind, Namespace: u.GetNamespace(), Name: u.GetName()},
				APIVersion: gvr.GroupVersion().String(), UID: u.GetUID(), ResourceVersion: u.GetResourceVersion(),
				Owners: u.GetOwnerReferences(), DeletionTimestamp: u.GetDeletionTimestamp(), Finalizers: u.GetFinalizers(),
			})
		}
	}
	for resource, b := range typed {
		list(schema.GroupVersionResource{Group: b.Group, Version: b.Version, Resource: resource}, b.GroupKind(), false)
	}
	for _, gvr := range watched {
		list(gvr, resourceid.GroupKind{Group: gvr.Group, Kind: served[gvr.GroupResource()].Kind}, true)
	}

	q := ownershiptest.Queries{Domains: []ownership.Domain{ownership.Everywhere()}, Limits: []int{0}}
	uids := map[string]bool{}
	namespaces := map[string]bool{}
	for _, o := range w.Objects {
		q.Refs = append(q.Refs, o.Ref)
		for _, ref := range o.Owners {
			if !uids[string(ref.UID)] {
				uids[string(ref.UID)] = true
				q.UIDs = append(q.UIDs, ref.UID)
			}
		}
		if o.Ref.Namespace != "" && !namespaces[o.Ref.Namespace] && len(namespaces) < 12 {
			namespaces[o.Ref.Namespace] = true
			q.Domains = append(q.Domains, ownership.InNamespace(o.Ref.Namespace))
		}
	}
	want, _ := ownershiptest.Scan(w).Begin()
	got, err := cachesource.New(func() cachesource.Caches {
		return cachesource.Caches{Typed: typedCache, Dynamic: dynamicCache, Discovery: discovery}
	}).Begin()
	if err != nil {
		t.Fatal(err)
	}
	diffs := ownershiptest.Differences(want, got, q)

	// An object whose UID or resource version the cache and the list
	// disagree on changed between the two reads.
	listed := map[resourceid.Ref]string{}
	for _, o := range w.Objects {
		listed[o.Ref] = string(o.UID) + "@" + o.ResourceVersion
	}
	changed := func(ref resourceid.Ref) bool {
		read := got.Object(ref)
		cached := ""
		if read.State == ownership.Present {
			cached = string(read.Object.UID) + "@" + read.Object.ResourceVersion
		}
		return cached != listed[ref]
	}
	var unexplained []ownershiptest.Difference
	churn := 0
	for _, d := range diffs {
		if len(d.Objects) > 0 && !slices.ContainsFunc(d.Objects, func(r resourceid.Ref) bool { return !changed(r) }) {
			churn++
			continue
		}
		unexplained = append(unexplained, d)
	}
	resolutions, resolveDiffs := 0, 0
	for _, o := range w.Objects {
		dependent := want.Object(o.Ref)
		if dependent.State != ownership.Present || changed(o.Ref) {
			continue
		}
		for _, ref := range dependent.Object.OwnerReferences {
			resolutions++
			a, b := ownership.ResolveOwner(want, dependent.Object, ref), ownership.ResolveOwner(got, dependent.Object, ref)
			if (a.State != b.State || a.Ref != b.Ref || a.Gap != b.Gap) && !changed(a.Ref) {
				resolveDiffs++
				t.Errorf("ResolveOwner(%v, %s %s/%s): scan %v %+v, cache %v %+v", o.Ref, ref.APIVersion, ref.Kind, ref.Name, a.State, a.Gap, b.State, b.Gap)
			}
		}
	}
	if err := got.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
	questions := len(q.Refs) + len(q.UIDs)*len(q.Domains)
	t.Logf("%d objects, %d questions (%d objects, %d owner UIDs × %d domains), %d owner references resolved", len(w.Objects), questions, len(q.Refs), len(q.UIDs), len(q.Domains), resolutions)
	t.Logf("differences: %d explained by objects changing between the cache's view and the list, %d unexplained; owner resolutions differing: %d", churn, len(unexplained), resolveDiffs)
	for i, d := range unexplained {
		if i == 20 {
			t.Errorf("... and %d more", len(unexplained)-20)
			break
		}
		t.Errorf("%s", strings.TrimSpace(fmt.Sprint(d)))
	}
}
