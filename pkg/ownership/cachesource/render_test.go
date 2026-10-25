package cachesource

import (
	"errors"
	"strings"
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
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/resourceid"
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

func setMeta(t testing.TB, obj runtime.Object, o ownershiptest.Object) {
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
	for _, ref := range o.OwnerReferences {
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
func apiObject(t testing.TB, o ownershiptest.Object) (runtime.Object, bool) {
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
	switch o.Ref.GroupKind() {
	case ownership.ArgoApplication:
		renderApplication(u, o)
	case ownership.FluxHelmRelease, ownership.FluxKustomization:
		renderFluxManager(u, o)
	}
	return u, false
}

// namespacedKind reports whether the World serves gk as namespaced, and
// whether it serves gk at all.
func namespacedKind(gk resourceid.GroupKind) (namespaced, known bool) {
	k, known := ownershiptest.KindOf(gk)
	return k.Namespaced, known
}

// renderApplication writes the spec and status by which an Argo CD
// Application says what o means, in the form o.Form picks. A member in the
// destination namespace is written without its namespace every other entry,
// as Argo CD may.
func renderApplication(u *unstructured.Unstructured, o ownershiptest.Object) {
	entries := []any{}
	for i, m := range o.Members {
		e := map[string]any{"kind": m.Kind, "name": m.Name, "version": "v1", "status": "Synced"}
		if m.Group != "" {
			e["group"] = m.Group
		}
		namespaced, known := namespacedKind(m.GroupKind())
		inDestination := known && namespaced && o.DestinationNamespace != "" && m.Namespace == o.DestinationNamespace && i%2 == 1
		if m.Namespace != "" && !inDestination {
			e["namespace"] = m.Namespace
		}
		entries = append(entries, e)
	}
	destination := map[string]any{"server": "https://kubernetes.default.svc"}
	if o.Form == 1 {
		destination = map[string]any{"name": "in-cluster"}
	}
	status := map[string]any{"reconciledAt": "2026-10-17T10:00:00Z", "sync": map[string]any{"status": "Synced"}, "resources": entries}
	switch o.Membership {
	case ownership.MembersRecorded:
		if len(entries) == 0 && o.Form != 0 {
			// Argo CD leaves an empty list out.
			delete(status, "resources")
		}
	case ownership.MembersRemote:
		destination = map[string]any{"server": "https://prod.example.com:6443"}
		if o.Form == 1 {
			destination = map[string]any{"name": "prod"}
		}
	case ownership.MembersUnreadable:
		switch o.Form {
		case 0: // never reconciled
			status = nil
		case 1: // an entry naming no object
			status["resources"] = append(entries, map[string]any{"kind": "ConfigMap", "namespace": "alpha"})
		default: // no destination
			destination = map[string]any{}
		}
	}
	if o.DestinationNamespace != "" && len(destination) > 0 {
		destination["namespace"] = o.DestinationNamespace
	}
	u.Object["spec"] = map[string]any{"project": "default", "destination": destination}
	if status != nil {
		u.Object["status"] = status
	}
}

// renderFluxManager writes the spec and status by which a Flux Kustomization
// or HelmRelease says what o means, in the form o.Form picks. A
// cluster-scoped member is written with the manager's namespace every other
// entry, as Flux may record a namespace the kind ignores, and a colon in an
// RBAC member's name as the double underscore Flux writes.
func renderFluxManager(u *unstructured.Unstructured, o ownershiptest.Object) {
	entries := []any{}
	for i, m := range o.Members {
		namespace := m.Namespace
		if namespaced, known := namespacedKind(m.GroupKind()); known && !namespaced && i%2 == 1 {
			namespace = o.Ref.Namespace
		}
		name := m.Name
		if m.Group == "rbac.authorization.k8s.io" {
			name = strings.ReplaceAll(name, ":", "__")
		}
		entries = append(entries, map[string]any{"id": strings.Join([]string{namespace, name, m.Group, m.Kind}, "_"), "v": "v1"})
	}
	spec := map[string]any{"interval": "10m"}
	inventory := map[string]any{"entries": entries}
	switch o.Membership {
	case ownership.MembersRecorded:
		if len(entries) == 0 && o.Form != 0 {
			inventory = map[string]any{}
		}
	case ownership.MembersRemote:
		spec["kubeConfig"] = map[string]any{"secretRef": map[string]any{"name": "prod-kubeconfig"}}
	case ownership.MembersUnreadable:
		switch o.Form {
		case 0: // not applied yet, or a controller that records no inventory
			inventory = nil
		case 1: // an entry whose ID doesn't parse
			inventory["entries"] = append(entries, map[string]any{"id": "not-an-id", "v": "v1"})
		default: // an entry without an ID
			inventory["entries"] = append(entries, map[string]any{"v": "v1"})
		}
	}
	u.Object["spec"] = spec
	status := map[string]any{"observedGeneration": int64(1)}
	if inventory != nil {
		status["inventory"] = inventory
	}
	u.Object["status"] = status
}

// render starts Radar's caches over fake clients holding w, collected as
// w.Collection says, and returns once every collected store has loaded.
func render(t testing.TB, w ownershiptest.World) *rendered {
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

	// Reactors go in before any watch starts: the fake client's reactor
	// chain isn't safe to change while informers list through it.
	for _, k := range ownershiptest.Kinds {
		if !k.Typed && k.HasGCVerbs {
			r.reactDynamic(k, w.Collection[k.GroupKind])
		}
	}
	for _, k := range ownershiptest.Kinds {
		if !k.Typed && k.HasGCVerbs {
			r.watchDynamic(t, k, w.Collection[k.GroupKind])
		}
	}
	return r
}

// reactDynamic makes the fake API serve k as c says: refusing it, or failing
// the informers' lists after each scope's access probe while it is loading.
func (r *rendered) reactDynamic(k ownershiptest.Kind, c ownershiptest.Collection) {
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
		return
	}
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
}

// watchDynamic has the dynamic cache watch k as c says, and waits for the
// watches c says have loaded.
func (r *rendered) watchDynamic(t testing.TB, k ownershiptest.Kind, c ownershiptest.Collection) {
	t.Helper()
	gvr := schema.GroupVersionResource{Group: k.Group, Version: k.Version, Resource: k.Resource}
	switch c.State {
	case ownershiptest.NotCollected:
		return
	case ownershiptest.Refused:
		if err := r.caches.Dynamic.EnsureWatching(gvr); err == nil {
			t.Fatalf("render: %v's watch should have been refused", gvr)
		}
		return
	}
	scopes := []string{""}
	if k.Namespaced && c.Namespaces != nil {
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
