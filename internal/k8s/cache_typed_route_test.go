package k8s

import (
	"context"
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
)

func TestTypedRouteGVRDispatch(t *testing.T) {
	cases := []struct {
		kind, group string
		want        bool
	}{
		{"Deployment", "apps", true},
		{"Deployment", "", true},
		{"deployment", "apps", true},
		{"Service", "", true},
		{"Service", "serving.knative.dev", false}, // Knative Service stays dynamic
		{"ConfigMap", "", true},
		{"Prometheus", "monitoring.coreos.com", false},
		{"Endpoints", "", false},      // typed=false: bypass kind stays dynamic
		{"EndpointSlice", "discovery.k8s.io", false},
		{"ClusterRole", "rbac.authorization.k8s.io", true},
		{"Application", "argoproj.io", false},
	}
	for _, tc := range cases {
		if _, got := typedRouteGVR(tc.kind, tc.group); got != tc.want {
			t.Errorf("typedRouteGVR(%q, %q) = %v, want %v", tc.kind, tc.group, got, tc.want)
		}
	}
}

// GitOps tree/insights enrichment reads built-in kinds through the dynamic
// accessors. Those reads must come from the typed cache — a dynamic informer
// for a typed kind duplicates a cluster-wide watch (double memory for
// Secrets/ConfigMaps) and its serial startup was the dominant cold-path cost
// of the GitOps detail page.
func TestTypedKindsServedFromTypedCacheWithoutDynamicInformer(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()

	client := fakeclientset.NewSimpleClientset(
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "billing", Namespace: "prod"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: "prod"}},
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "reader"}},
	)
	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{})
	if err := InitTestDynamicResourceCache(dyn, nil); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	cache := GetResourceCache()

	dep, err := cache.GetDynamicWithGroup(context.Background(), "Deployment", "prod", "billing", "apps")
	if err != nil {
		t.Fatalf("GetDynamicWithGroup(Deployment): %v", err)
	}
	if dep.GetAPIVersion() != "apps/v1" || dep.GetKind() != "Deployment" {
		t.Fatalf("Deployment GVK = %s/%s, want apps/v1/Deployment", dep.GetAPIVersion(), dep.GetKind())
	}

	// Empty group means "the built-in" across the server's dispatch.
	if _, err := cache.GetDynamicWithGroup(context.Background(), "Deployment", "prod", "billing", ""); err != nil {
		t.Fatalf("GetDynamicWithGroup(Deployment, no group): %v", err)
	}

	cms, err := cache.ListDynamicWithGroup(context.Background(), "ConfigMap", "prod", "")
	if err != nil {
		t.Fatalf("ListDynamicWithGroup(ConfigMap): %v", err)
	}
	if len(cms) != 1 || cms[0].GetKind() != "ConfigMap" || cms[0].GetAPIVersion() != "v1" {
		t.Fatalf("ConfigMap list = %#v, want one item with v1/ConfigMap GVK", cms)
	}

	cr, err := cache.GetDynamicWithGroup(context.Background(), "ClusterRole", "", "reader", "rbac.authorization.k8s.io")
	if err != nil {
		t.Fatalf("GetDynamicWithGroup(ClusterRole): %v", err)
	}
	if cr.GetKind() != "ClusterRole" {
		t.Fatalf("ClusterRole kind = %q", cr.GetKind())
	}

	if count := GetDynamicResourceCache().GetInformerCount(); count != 0 {
		t.Fatalf("typed-kind reads started %d dynamic informer(s), want 0", count)
	}
}

// The typed informer transform strips last-applied only for kinds in its
// explicit switch — RBAC kinds fall through with the annotation intact.
// The typed route must strip it on the way out or every tree/list payload
// leaks a full JSON copy of the object (the dynamic path never exposes it).
func TestTypedRouteStripsLastAppliedAndManagedFields(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()

	client := fakeclientset.NewSimpleClientset(
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{
			Name: "leaky",
			Annotations: map[string]string{
				"kubectl.kubernetes.io/last-applied-configuration": `{"kind":"ClusterRole"}`,
				"keep": "me",
			},
		}},
	)
	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{})
	if err := InitTestDynamicResourceCache(dyn, nil); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}

	got, err := GetResourceCache().GetDynamicWithGroup(context.Background(), "ClusterRole", "", "leaky", "rbac.authorization.k8s.io")
	if err != nil {
		t.Fatalf("GetDynamicWithGroup: %v", err)
	}
	if _, ok := got.GetAnnotations()["kubectl.kubernetes.io/last-applied-configuration"]; ok {
		t.Fatal("typed route leaked kubectl last-applied to an outward reader")
	}
	if got.GetAnnotations()["keep"] != "me" {
		t.Fatalf("other annotations must survive the strip: %v", got.GetAnnotations())
	}
	if _, found, _ := unstructured.NestedSlice(got.Object, "metadata", "managedFields"); found {
		t.Fatal("typed route leaked managedFields")
	}
}

func TestTypedKindNotFoundMapsToAPINotFound(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()

	if err := InitTestResourceCache(fakeclientset.NewSimpleClientset()); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{})
	if err := InitTestDynamicResourceCache(dyn, nil); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}

	_, err := GetResourceCache().GetDynamicWithGroup(context.Background(), "Deployment", "prod", "missing", "apps")
	if err == nil {
		t.Fatal("expected NotFound error")
	}
	if !apierrors.IsNotFound(err) {
		t.Fatalf("error %v is not an apierrors NotFound; HTTP handlers map that to 404", err)
	}
}

// A cluster-scoped typed kind listed with a namespace filter must return
// empty (the dynamic cache's namespace index behavior), not the full
// cluster-wide set FetchResourceList would produce.
func TestClusterScopedTypedListWithNamespaceFilterReturnsEmpty(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()

	client := fakeclientset.NewSimpleClientset(
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "reader"}},
	)
	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{})
	if err := InitTestDynamicResourceCache(dyn, nil); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}

	items, err := GetResourceCache().ListDynamicWithGroup(context.Background(), "ClusterRole", "prod", "rbac.authorization.k8s.io")
	if err != nil {
		t.Fatalf("ListDynamicWithGroup: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("namespace-filtered cluster-scoped list returned %d items, want 0", len(items))
	}
}

// Drift needs kubectl last-applied, which the typed cache strips at
// ingestion. The preserve variant must therefore keep its direct-GET path
// even for typed kinds — routing it typed would silently report "no drift"
// for every built-in.
func TestPreserveLastAppliedBypassesTypedCache(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()

	lastApplied := `{"kind":"Deployment"}`
	client := fakeclientset.NewSimpleClientset(
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "billing", Namespace: "prod"}},
	)
	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	liveDep := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"name":      "billing",
			"namespace": "prod",
			"annotations": map[string]any{
				"kubectl.kubernetes.io/last-applied-configuration": lastApplied,
			},
		},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "DeploymentList"},
		liveDep,
	)
	if err := InitTestDynamicResourceCache(dyn, []APIResource{{
		Group: "apps", Version: "v1", Kind: "Deployment", Name: "deployments", Namespaced: true,
	}}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}

	got, err := GetResourceCache().GetDynamicWithGroupPreserveLastApplied(context.Background(), "Deployment", "prod", "billing", "apps")
	if err != nil {
		t.Fatalf("GetDynamicWithGroupPreserveLastApplied: %v", err)
	}
	if got.GetAnnotations()["kubectl.kubernetes.io/last-applied-configuration"] != lastApplied {
		t.Fatalf("last-applied not preserved — the preserve path was routed through the typed cache: %v", got.GetAnnotations())
	}
	if count := GetDynamicResourceCache().GetInformerCount(); count != 0 {
		t.Fatalf("preserve path started %d dynamic informer(s), want direct GET", count)
	}
}

// Every kind the typed table marks typed=true must be handled by both
// FetchResource and FetchResourceList — a table entry without a switch case
// (IngressClass was one) silently regresses that kind to "unknown" once the
// dynamic accessors route typed-first.
func TestTypedBuiltinTableParityWithFetchSwitches(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()

	if err := InitTestResourceCache(fakeclientset.NewSimpleClientset()); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()

	seen := map[string]bool{}
	for form, gvr := range typedBuiltinGVRs {
		if seen[gvr.Resource] {
			continue
		}
		seen[gvr.Resource] = true

		if _, err := FetchResource(cache, form, "ns", "name"); errors.Is(err, ErrUnknownKind) {
			t.Errorf("FetchResource(%q → %s) returned ErrUnknownKind; typed table and switch are out of sync", form, gvr.Resource)
		}
		if _, err := FetchResourceList(cache, form, nil); errors.Is(err, ErrUnknownKind) {
			t.Errorf("FetchResourceList(%q → %s) returned ErrUnknownKind; typed table and switch are out of sync", form, gvr.Resource)
		}
		if _, ok := builtinKindForResource(gvr.Resource); !ok {
			t.Errorf("builtinKindForResource(%q) missing; converted objects would lack a kind", gvr.Resource)
		}
	}
}
