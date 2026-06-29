package k8score

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

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
