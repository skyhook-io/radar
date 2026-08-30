package k8score

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
)

type countingDiscovery struct {
	discovery.DiscoveryInterface
	calls atomic.Int32
}

func (d *countingDiscovery) ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	d.calls.Add(1)
	return d.DiscoveryInterface.ServerGroupsAndResources()
}

func TestRefreshIfStaleCoalescesConcurrentRefreshes(t *testing.T) {
	fakeDiscovery := fakeclientset.NewSimpleClientset().Discovery().(*fakediscovery.FakeDiscovery)
	fakeDiscovery.Resources = []*metav1.APIResourceList{{
		GroupVersion: "metrics.k8s.io/v1",
		APIResources: []metav1.APIResource{{Name: "pods", Kind: "PodMetrics"}},
	}}
	client := &countingDiscovery{DiscoveryInterface: fakeDiscovery}
	d, err := NewResourceDiscovery(client, WithDiscoveryCacheTTL(time.Hour))
	if err != nil {
		t.Fatalf("NewResourceDiscovery: %v", err)
	}
	client.calls.Store(0)
	d.mu.Lock()
	d.lastRefresh = time.Now().Add(-2 * time.Hour)
	d.mu.Unlock()

	const callers = 20
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- d.RefreshIfStale()
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("RefreshIfStale: %v", err)
		}
	}
	if got := client.calls.Load(); got != 1 {
		t.Fatalf("ServerGroupsAndResources calls = %d, want 1", got)
	}
}

func TestRefreshPreservesSnapshotOnEmptyDiscoveryError(t *testing.T) {
	initial := []*metav1.APIResourceList{{
		GroupVersion: "metrics.k8s.io/v1",
		APIResources: []metav1.APIResource{{Name: "pods", Kind: "PodMetrics"}},
	}}
	client := &snapshotDiscovery{resources: initial}
	d, err := NewResourceDiscovery(client)
	if err != nil {
		t.Fatalf("NewResourceDiscovery: %v", err)
	}

	client.resources = nil
	client.err = errors.New("temporary discovery failure")
	beforeRefresh := d.Stats().LastRefresh
	if err := d.Refresh(); err == nil {
		t.Fatal("Refresh unexpectedly succeeded")
	}
	gvr, ok := d.GetGVRWithGroup("pods", MetricsAPIGroup)
	if !ok || gvr.Version != "v1" {
		t.Fatalf("preserved metrics GVR = %v, ok=%v, want metrics.k8s.io/v1", gvr, ok)
	}
	if !d.Stats().LastRefresh.After(beforeRefresh) {
		t.Fatal("failed refresh did not advance the retry cooldown")
	}
}

type snapshotDiscovery struct {
	discovery.DiscoveryInterface
	resources []*metav1.APIResourceList
	err       error
}

func (d *snapshotDiscovery) ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	return nil, d.resources, d.err
}

func TestAddAPIResourceRegistersGroupQualifiedCRD(t *testing.T) {
	d := &ResourceDiscovery{
		resourceMap: make(map[string]APIResource),
		gvrMap:      make(map[string]schema.GroupVersionResource),
	}

	d.AddAPIResource(APIResource{
		Group:      "networking.istio.io",
		Version:    "v1",
		Kind:       "VirtualService",
		Name:       "virtualservices",
		Namespaced: true,
		IsCRD:      true,
		Verbs:      []string{"get", "list", "watch"},
	})

	gvr, ok := d.GetGVRWithGroup("VirtualService", "networking.istio.io")
	if !ok {
		t.Fatal("expected group-qualified VirtualService lookup to resolve")
	}
	if gvr != (schema.GroupVersionResource{Group: "networking.istio.io", Version: "v1", Resource: "virtualservices"}) {
		t.Fatalf("GVR = %v, want networking.istio.io/v1 virtualservices", gvr)
	}
	if !d.SupportsWatchGVR(gvr) {
		t.Fatal("expected exact fallback GVR to support watch")
	}
	if got := d.GetKindForGVR(gvr); got != "VirtualService" {
		t.Fatalf("kind = %q, want VirtualService", got)
	}
}

func TestGetGVRWithGroupPrefersMostStableVersion(t *testing.T) {
	d := &ResourceDiscovery{
		resourceMap: make(map[string]APIResource),
		gvrMap:      make(map[string]schema.GroupVersionResource),
	}

	// Both versions served; deprecated v1beta1 discovered first. Informers are
	// registered against the storage version (v1), so the count lookup must
	// resolve to v1 — not the first-discovered v1beta1.
	d.AddAPIResource(APIResource{
		Group:      "gateway.networking.k8s.io",
		Version:    "v1beta1",
		Kind:       "Gateway",
		Name:       "gateways",
		Namespaced: true,
		IsCRD:      true,
		Verbs:      []string{"get", "list", "watch"},
	})
	d.AddAPIResource(APIResource{
		Group:      "gateway.networking.k8s.io",
		Version:    "v1",
		Kind:       "Gateway",
		Name:       "gateways",
		Namespaced: true,
		IsCRD:      true,
		Verbs:      []string{"get", "list", "watch"},
	})

	gvr, ok := d.GetGVRWithGroup("Gateway", "gateway.networking.k8s.io")
	if !ok {
		t.Fatal("expected group-qualified Gateway lookup to resolve")
	}
	if gvr.Version != "v1" {
		t.Fatalf("version = %q, want v1 (storage version, not first-discovered v1beta1)", gvr.Version)
	}
}

func TestSchedulingPodGroupPrefersBetaAndStaysGroupQualified(t *testing.T) {
	d := &ResourceDiscovery{
		resourceMap: make(map[string]APIResource),
		gvrMap:      make(map[string]schema.GroupVersionResource),
	}
	for _, resource := range []APIResource{
		{Group: "scheduling.k8s.io", Version: "v1alpha3", Kind: "PodGroup", Name: "podgroups", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
		{Group: "scheduling.k8s.io", Version: "v1beta1", Kind: "PodGroup", Name: "podgroups", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
		{Group: "example.io", Version: "v1", Kind: "PodGroup", Name: "custompodgroups", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
		{Group: "scheduling.k8s.io", Version: "v1alpha3", Kind: "CompositePodGroup", Name: "compositepodgroups", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
	} {
		d.AddAPIResource(resource)
	}

	podGroup, ok := d.GetGVRWithGroup("PodGroup", "scheduling.k8s.io")
	if !ok || podGroup != (schema.GroupVersionResource{Group: "scheduling.k8s.io", Version: "v1beta1", Resource: "podgroups"}) {
		t.Fatalf("scheduling PodGroup GVR = %v, ok=%v", podGroup, ok)
	}
	custom, ok := d.GetGVRWithGroup("PodGroup", "example.io")
	if !ok || custom.Resource != "custompodgroups" {
		t.Fatalf("custom PodGroup GVR = %v, ok=%v", custom, ok)
	}
	composite, ok := d.GetGVRWithGroup("CompositePodGroup", "scheduling.k8s.io")
	if !ok || composite.Version != "v1alpha3" {
		t.Fatalf("CompositePodGroup GVR = %v, ok=%v", composite, ok)
	}
}

func TestSupportsWatchGVRUsesExactGroupVersionResource(t *testing.T) {
	d := &ResourceDiscovery{
		resourceMap: make(map[string]APIResource),
		gvrMap:      make(map[string]schema.GroupVersionResource),
	}

	d.AddAPIResource(APIResource{
		Group:      "gateway.networking.k8s.io",
		Version:    "v1",
		Kind:       "Gateway",
		Name:       "gateways",
		Namespaced: true,
		IsCRD:      true,
		Verbs:      []string{"get", "list", "watch"},
	})
	d.AddAPIResource(APIResource{
		Group:      "networking.istio.io",
		Version:    "v1",
		Kind:       "Gateway",
		Name:       "gateways",
		Namespaced: true,
		IsCRD:      true,
		Verbs:      []string{"get", "list"},
	})

	gatewayAPI := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}
	istio := schema.GroupVersionResource{Group: "networking.istio.io", Version: "v1", Resource: "gateways"}
	if !d.SupportsWatchGVR(gatewayAPI) {
		t.Fatal("Gateway API GVR should support watch")
	}
	if d.SupportsWatchGVR(istio) {
		t.Fatal("Istio Gateway GVR should not inherit watch support from Gateway API Gateway")
	}
	if got := d.GetKindForGVR(istio); got != "Gateway" {
		t.Fatalf("kind = %q, want Gateway", got)
	}
}

func TestAddAPIResourceUpdatesExistingGVR(t *testing.T) {
	d := &ResourceDiscovery{
		resourceMap: make(map[string]APIResource),
		gvrMap:      make(map[string]schema.GroupVersionResource),
	}

	resource := APIResource{
		Group:      "keda.sh",
		Version:    "v1alpha1",
		Kind:       "ScaledObject",
		Name:       "scaledobjects",
		Namespaced: true,
		IsCRD:      true,
		Verbs:      []string{"get", "list"},
	}
	d.AddAPIResource(resource)
	resource.Verbs = []string{"get", "list", "watch"}
	d.AddAPIResource(resource)

	resources, err := d.GetAPIResources()
	if err != nil {
		t.Fatalf("GetAPIResources failed: %v", err)
	}
	if len(resources) != 1 {
		t.Fatalf("resource count = %d, want 1", len(resources))
	}
	gvr := schema.GroupVersionResource{Group: "keda.sh", Version: "v1alpha1", Resource: "scaledobjects"}
	if !d.SupportsWatchGVR(gvr) {
		t.Fatal("updated GVR should support watch")
	}
}

func TestSupportsWatchGVRCoreResourceUsesExactEmptyGroup(t *testing.T) {
	d := &ResourceDiscovery{
		resourceMap: make(map[string]APIResource),
		gvrMap:      make(map[string]schema.GroupVersionResource),
	}

	d.AddAPIResource(APIResource{
		Group:      "",
		Version:    "v1",
		Kind:       "Service",
		Name:       "services",
		Namespaced: true,
		IsCRD:      false,
		Verbs:      []string{"get", "list", "watch"},
	})
	d.AddAPIResource(APIResource{
		Group:      "serving.knative.dev",
		Version:    "v1",
		Kind:       "Service",
		Name:       "services",
		Namespaced: true,
		IsCRD:      true,
		Verbs:      []string{"get", "list"},
	})

	core := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "services"}
	knative := schema.GroupVersionResource{Group: "serving.knative.dev", Version: "v1", Resource: "services"}
	if !d.SupportsWatchGVR(core) {
		t.Fatal("core Service GVR should support watch")
	}
	if d.SupportsWatchGVR(knative) {
		t.Fatal("Knative Service GVR should not inherit watch support from core Service")
	}
}

// TestGetGVRBareKindPrefersListWatchAcrossGroups covers the kueue LocalQueue
// collision: the plural "localqueues" exists as a real CRD in kueue.x-k8s.io
// (supports list/watch) and as kueue's aggregated visibility APIService in
// visibility.kueue.x-k8s.io (no list/watch). Both are CRD-typed and live in
// different groups, so the bare-kind pick is decided by the list/watch verbs:
// GetGVR must resolve to the list/watch-capable CRD regardless of insertion
// order, since cross-group discovery order is unstable in production.
func TestGetGVRBareKindPrefersListWatchAcrossGroups(t *testing.T) {
	visibility := APIResource{
		Group:      "visibility.kueue.x-k8s.io",
		Version:    "v1beta2",
		Kind:       "LocalQueue",
		Name:       "localqueues",
		Namespaced: true,
		IsCRD:      true,
		Verbs:      []string{"get"},
	}
	realCRD := APIResource{
		Group:      "kueue.x-k8s.io",
		Version:    "v1beta1",
		Kind:       "LocalQueue",
		Name:       "localqueues",
		Namespaced: true,
		IsCRD:      true,
		Verbs:      []string{"get", "list", "watch"},
	}
	want := schema.GroupVersionResource{Group: "kueue.x-k8s.io", Version: "v1beta1", Resource: "localqueues"}

	cases := []struct {
		name  string
		first APIResource
		last  APIResource
	}{
		{name: "visibility discovered first", first: visibility, last: realCRD},
		{name: "real CRD discovered first", first: realCRD, last: visibility},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &ResourceDiscovery{
				resourceMap: make(map[string]APIResource),
				gvrMap:      make(map[string]schema.GroupVersionResource),
			}
			d.AddAPIResource(tc.first)
			d.AddAPIResource(tc.last)

			gvr, ok := d.GetGVR("localqueues")
			if !ok {
				t.Fatal("expected bare-kind localqueues lookup to resolve")
			}
			if gvr != want {
				t.Fatalf("GetGVR(localqueues) = %v, want %v (list/watch-capable CRD)", gvr, want)
			}
		})
	}
}

// A nil *discovery.DiscoveryClient handed to this constructor makes a non-nil
// interface value, so a plain `client == nil` guard passes and the first call
// on the client segfaults. This crashed the whole internal/k8s test binary in
// CI, where discovery starts before any client exists.
func TestNewResourceDiscoveryRejectsATypedNilClient(t *testing.T) {
	var client *discovery.DiscoveryClient // nil, but not a nil interface

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("constructing with a typed-nil client must return an error, not panic: %v", r)
		}
	}()

	rd, err := NewResourceDiscovery(client)
	if err == nil {
		t.Fatal("expected an error for a nil discovery client")
	}
	if rd != nil {
		t.Errorf("expected no discovery on error, got %#v", rd)
	}
}

func TestNewResourceDiscoveryRejectsAPlainNilClient(t *testing.T) {
	if _, err := NewResourceDiscovery(nil); err == nil {
		t.Fatal("expected an error for a nil discovery client")
	}
}
