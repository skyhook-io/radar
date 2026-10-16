package cachesource

import (
	"errors"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
)

// rendered is a World served by fake API clients, with Radar's caches over
// them.
type rendered struct {
	typed   *fake.Clientset
	dynamic *dynamicfake.FakeDynamicClient
	caches  Caches
}

// worldDiscovery serves a World's resources, failing one group when the World
// says discovery was partial.
type worldDiscovery struct {
	discovery.DiscoveryInterface
	lists []*metav1.APIResourceList
	err   error
}

func (d *worldDiscovery) ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	return nil, d.lists, d.err
}

func newWorldDiscovery(w ownershiptest.World) *worldDiscovery {
	byGroupVersion := map[string]*metav1.APIResourceList{}
	d := &worldDiscovery{}
	for _, r := range w.Resources {
		gv := schema.GroupVersion{Group: r.Group, Version: r.Version}.String()
		list := byGroupVersion[gv]
		if list == nil {
			list = &metav1.APIResourceList{GroupVersion: gv}
			byGroupVersion[gv] = list
			d.lists = append(d.lists, list)
		}
		list.APIResources = append(list.APIResources, metav1.APIResource{Name: r.Resource, Kind: r.Kind, Namespaced: r.Namespaced, Verbs: r.Verbs})
	}
	if w.Partial {
		d.err = &discovery.ErrGroupDiscoveryFailed{Groups: map[schema.GroupVersion]error{
			{Group: "metrics.k8s.io", Version: "v1beta1"}: errors.New("service unavailable"),
		}}
	}
	return d
}

func setMeta(t *testing.T, obj runtime.Object, o ownershiptest.Object) {
	t.Helper()
	m, err := apimeta.Accessor(obj)
	if err != nil {
		t.Fatal(err)
	}
	m.SetNamespace(o.Ref.Namespace)
	m.SetName(o.Ref.Name)
	m.SetUID(o.UID)
	m.SetResourceVersion(o.ResourceVersion)
	var owners []metav1.OwnerReference
	for _, ref := range o.Owners {
		owners = append(owners, *ref.DeepCopy())
	}
	m.SetOwnerReferences(owners)
	if o.DeletionTimestamp != nil {
		m.SetDeletionTimestamp(o.DeletionTimestamp.DeepCopy())
	}
	m.SetFinalizers(o.Finalizers)
}

// apiObject renders o as the API serves it: a typed object for a kind Radar's
// typed cache holds, unstructured otherwise.
func apiObject(t *testing.T, o ownershiptest.Object) (runtime.Object, bool) {
	t.Helper()
	k, ok := ownershiptest.KindOf(o.Ref.GroupKind())
	if !ok || o.APIVersion != k.APIVersion() {
		t.Fatalf("render: %v at %s is not a generated kind's version", o.Ref, o.APIVersion)
	}
	gvk := schema.GroupVersionKind{Group: k.Group, Version: k.Version, Kind: k.Kind}
	if k.Typed {
		obj, err := scheme.Scheme.New(gvk)
		if err != nil {
			t.Fatal(err)
		}
		setMeta(t, obj, o)
		return obj, true
	}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	setMeta(t, u, o)
	return u, false
}

// render starts Radar's caches over fake clients holding w, collected as
// w.Collection says, and returns once every collected store has loaded.
func render(t *testing.T, w ownershiptest.World) *rendered {
	t.Helper()
	var typedObjects, dynamicObjects []runtime.Object
	for _, o := range w.Objects {
		obj, typed := apiObject(t, o)
		if typed {
			typedObjects = append(typedObjects, obj)
		} else {
			dynamicObjects = append(dynamicObjects, obj)
		}
	}

	r := &rendered{typed: fake.NewClientset(typedObjects...)}
	listKinds := map[schema.GroupVersionResource]string{}
	for _, k := range ownershiptest.Kinds {
		if !k.Typed {
			listKinds[schema.GroupVersionResource{Group: k.Group, Version: k.Version, Resource: k.Resource}] = k.Kind + "List"
		}
	}
	r.dynamic = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, dynamicObjects...)

	held := func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("initial list held")
	}
	scopes := map[string]k8score.ResourceScope{}
	scopeNamespaces := map[string][]string{}
	deferred := map[string]bool{}
	for _, k := range ownershiptest.Kinds {
		if !k.Typed {
			continue
		}
		c := w.Collection[k.GroupKind]
		switch c.State {
		case ownershiptest.NotCollected:
			continue
		case ownershiptest.Refused:
			scopes[k.Resource] = k8score.ResourceScope{}
			continue
		case ownershiptest.Unwatchable:
			t.Fatalf("render: typed %v can always be watched", k.GroupKind)
		case ownershiptest.Loading:
			deferred[k.Resource] = true
			r.typed.PrependReactor("list", k.Resource, held)
		}
		scope := k8score.ResourceScope{Enabled: true}
		if k.Namespaced && c.Namespaces != nil {
			scope.Namespace = c.Namespaces[0]
			if len(c.Namespaces) > 1 {
				scopeNamespaces[k.Resource] = c.Namespaces
			}
		}
		scopes[k.Resource] = scope
	}

	discoveryCache, err := k8score.NewResourceDiscovery(newWorldDiscovery(w))
	if err != nil {
		t.Fatal(err)
	}
	typedCache, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client: r.typed, ResourceScopes: scopes, ResourceScopeNamespaces: scopeNamespaces, DeferredTypes: deferred,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(typedCache.Stop)
	dynamicCache, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{DynamicClient: r.dynamic, Discovery: discoveryCache})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dynamicCache.Stop)
	r.caches = Caches{Typed: typedCache, Dynamic: dynamicCache, Discovery: discoveryCache}

	for _, k := range ownershiptest.Kinds {
		if k.Typed || !k.Watchable {
			continue
		}
		r.collectDynamic(t, k, w.Collection[k.GroupKind])
	}
	return r
}

// collectDynamic has the dynamic cache watch k as c says, and waits for the
// watches c says have loaded.
func (r *rendered) collectDynamic(t *testing.T, k ownershiptest.Kind, c ownershiptest.Collection) {
	t.Helper()
	gvr := schema.GroupVersionResource{Group: k.Group, Version: k.Version, Resource: k.Resource}
	refuse := func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", errors.New("Radar may not list this"))
	}
	limited := k.Namespaced && c.Namespaces != nil
	switch c.State {
	case ownershiptest.NotCollected:
		return
	case ownershiptest.Refused:
		r.dynamic.PrependReactor("list", k.Resource, refuse)
		if err := r.caches.Dynamic.EnsureWatching(gvr); err == nil {
			t.Fatalf("render: %v's watch should have been refused", gvr)
		}
		return
	}
	// Each scope's first list is the watch's access probe; when the kind is
	// loading, the informer's own lists after it fail.
	var mu sync.Mutex
	lists := map[string]int{}
	r.dynamic.PrependReactor("list", k.Resource, func(a k8stesting.Action) (bool, runtime.Object, error) {
		if limited && a.GetNamespace() == "" {
			return refuse(a)
		}
		mu.Lock()
		lists[a.GetNamespace()]++
		n := lists[a.GetNamespace()]
		mu.Unlock()
		if c.State == ownershiptest.Loading && n > 1 {
			return true, nil, errors.New("initial list held")
		}
		return false, nil, nil
	})
	scopes := []string{""}
	if limited {
		scopes = c.Namespaces
	}
	for _, ns := range scopes {
		var err error
		if ns == "" {
			err = r.caches.Dynamic.EnsureWatching(gvr)
		} else {
			_, err = r.caches.Dynamic.List(gvr, ns)
		}
		if err != nil {
			t.Fatalf("render: watching %v in %q: %v", gvr, ns, err)
		}
	}
	if c.State == ownershiptest.Loading {
		return
	}
	deadline := time.Now().Add(5 * time.Second)
	for _, ns := range scopes {
		for !r.caches.Dynamic.IsNamespaceSynced(gvr, ns) {
			if time.Now().After(deadline) {
				t.Fatalf("render: %v in %q did not load", gvr, ns)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func (r *rendered) source() *Source {
	return New(func() Caches { return r.caches })
}
