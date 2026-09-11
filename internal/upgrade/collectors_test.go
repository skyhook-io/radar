package upgrade

import (
	"context"
	"errors"
	"slices"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestSchedulingV1Alpha2ResourceFilter(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resource metav1.APIResource
		want     bool
	}{
		{name: "workload", resource: metav1.APIResource{Name: "workloads", Kind: "Workload", Verbs: metav1.Verbs{"get", "list"}}, want: true},
		{name: "pod group", resource: metav1.APIResource{Name: "podgroups", Kind: "PodGroup", Verbs: metav1.Verbs{"list"}}, want: true},
		{name: "status subresource", resource: metav1.APIResource{Name: "workloads/status", Kind: "Workload", Verbs: metav1.Verbs{"get", "patch"}}},
		{name: "not listable", resource: metav1.APIResource{Name: "workloads", Kind: "Workload", Verbs: metav1.Verbs{"get"}}, want: true},
		{name: "unrelated kind", resource: metav1.APIResource{Name: "priorities", Kind: "Priority", Verbs: metav1.Verbs{"list"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSchedulingV1Alpha2Resource(tc.resource); got != tc.want {
				t.Fatalf("isSchedulingV1Alpha2Resource(%+v) = %v, want %v", tc.resource, got, tc.want)
			}
		})
	}
}

var testUpgradeSourceResources = []upgradeSourceResource{
	{gvr: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}, namespaced: true},
	{gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, namespaced: true},
	{gvr: schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}, namespaced: true},
	{gvr: schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}, namespaced: true},
	{gvr: schema.GroupVersionResource{Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"}, namespaced: true},
	{gvr: schema.GroupVersionResource{Group: "policy", Version: "v1", Resource: "poddisruptionbudgets"}, namespaced: true},
}

func TestCollectUpgradeSourceObjectsRetainsPartialEvidence(t *testing.T) {
	listKinds := make(map[schema.GroupVersionResource]string, len(testUpgradeSourceResources))
	for _, resource := range testUpgradeSourceResources {
		listKinds[resource.gvr] = "UpgradeSourceList"
	}
	pod := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name":      "api",
			"namespace": "default",
		},
	}}
	lastApplied := map[string]any{"kubectl.kubernetes.io/last-applied-configuration": `{"apiVersion":"networking.k8s.io/v1beta1","kind":"Ingress","metadata":{"name":"web","namespace":"default"}}`}
	ingress := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "Ingress",
		"metadata": map[string]any{"name": "web", "namespace": "default", "annotations": lastApplied},
	}}
	hpa := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "autoscaling/v2", "kind": "HorizontalPodAutoscaler",
		"metadata": map[string]any{"name": "api", "namespace": "default"},
	}}
	pdb := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "policy/v1", "kind": "PodDisruptionBudget",
		"metadata": map[string]any{"name": "api", "namespace": "default"},
	}}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, pod, ingress, hpa, pdb)
	client.PrependReactor("list", "networkpolicies", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden")
	})

	objects, unavailable := collectUpgradeSourceObjectsWithClient(context.Background(), client, nil, testUpgradeSourceResources)
	if len(objects) != 1 || objects[0].GetName() != "web" {
		t.Fatalf("partial objects = %#v, want only the Ingress with last-applied evidence", objects)
	}
	if len(unavailable) != 1 || unavailable[0] != "networkpolicies" {
		t.Fatalf("unavailable = %v, want [networkpolicies]", unavailable)
	}
	foundIngressEvidence := false
	for _, object := range objects {
		if object.GetName() == "web" && object.GetAnnotations()["kubectl.kubernetes.io/last-applied-configuration"] != "" {
			foundIngressEvidence = true
		}
	}
	if !foundIngressEvidence {
		t.Fatal("Ingress kubectl last-applied evidence was not retained")
	}
}

func TestUpgradeSourceExcludesControllerAuthoredHighCardinalityKinds(t *testing.T) {
	for _, kind := range []string{"Event", "Lease"} {
		if !upgradeSourceExcludedKinds[kind] {
			t.Fatalf("%s must not be listed for kubectl last-applied evidence", kind)
		}
	}
	if upgradeSourceExcludedKinds["EndpointSlice"] {
		t.Fatal("EndpointSlice source manifests remain relevant to strict IP validation")
	}
}

func TestCollectUpgradeSourceObjectsReturnsNilWhenEveryListFails(t *testing.T) {
	listKinds := make(map[schema.GroupVersionResource]string, len(testUpgradeSourceResources))
	for _, resource := range testUpgradeSourceResources {
		listKinds[resource.gvr] = "UpgradeSourceList"
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	client.PrependReactor("list", "*", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("unavailable")
	})

	objects, unavailable := collectUpgradeSourceObjectsWithClient(context.Background(), client, []string{"default"}, testUpgradeSourceResources)
	if objects != nil {
		t.Fatalf("objects = %#v, want nil when every list fails", objects)
	}
	if len(unavailable) != len(testUpgradeSourceResources) {
		t.Fatalf("unavailable = %v, want %d kinds", unavailable, len(testUpgradeSourceResources))
	}
}

func TestCollectUpgradeCRDsReturnsNilOnListFailure(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		crdGVR: "CustomResourceDefinitionList",
	})
	client.PrependReactor("list", "customresourcedefinitions", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("transient failure")
	})

	if crds := collectUpgradeCRDs(context.Background(), client); crds != nil {
		t.Fatalf("crds = %#v, want nil so the check reports incomplete evidence", crds)
	}
}

func TestCompactUpgradeCRDDropsValidationSchemas(t *testing.T) {
	crd := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition",
		"metadata": map[string]any{"name": "widgets.example.io"},
		"spec": map[string]any{
			"versions": []any{map[string]any{
				"name": "v1", "served": true, "storage": true,
				"schema": map[string]any{"openAPIV3Schema": map[string]any{"type": "object"}},
			}},
			"conversion": map[string]any{"strategy": "None"},
		},
		"status": map[string]any{"storedVersions": []any{"v1"}},
	}}
	compact := compactUpgradeCRD(crd)
	versions, _, _ := unstructured.NestedSlice(compact.Object, "spec", "versions")
	version := versions[0].(map[string]any)
	if _, found := version["schema"]; found || version["name"] != "v1" || version["storage"] != true {
		t.Fatalf("compact versions = %+v, want only conversion-relevant fields", versions)
	}
}

func TestCollectUpgradeAPIServicesPreservesNilAndEmptyEvidence(t *testing.T) {
	listKinds := map[schema.GroupVersionResource]string{apiServiceGVR: "APIServiceList"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)

	apiServices := collectUpgradeAPIServicesWithClient(context.Background(), client)
	if apiServices == nil || len(apiServices) != 0 {
		t.Fatalf("empty successful list = %#v, want non-nil empty evidence", apiServices)
	}

	client.PrependReactor("list", "apiservices", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden")
	})
	if apiServices := collectUpgradeAPIServicesWithClient(context.Background(), client); apiServices != nil {
		t.Fatalf("failed list = %#v, want nil incomplete evidence", apiServices)
	}
}

func TestCollectUpgradeAPIServicesReturnsObjects(t *testing.T) {
	apiService := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiregistration.k8s.io/v1",
		"kind":       "APIService",
		"metadata":   map[string]any{"name": "v1.example.io"},
	}}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		apiServiceGVR: "APIServiceList",
	}, apiService)
	got := collectUpgradeAPIServicesWithClient(context.Background(), client)
	if len(got) != 1 || got[0].GetName() != "v1.example.io" {
		t.Fatalf("APIServices = %#v, want v1.example.io", got)
	}
}

func TestCollectUpgradeWebhookConfigurationsSeparatesDeniedFromErrored(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		validatingWebhookGVR: "ValidatingWebhookConfigurationList",
		mutatingWebhookGVR:   "MutatingWebhookConfigurationList",
	})
	client.PrependReactor("list", "validatingwebhookconfigurations", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("transient apiserver failure")
	})
	authz := &fakeUpgradeAuthorizer{canList: map[string]bool{
		validatingWebhookGVR.Group + "/" + validatingWebhookGVR.Resource + "/": true,
		mutatingWebhookGVR.Group + "/" + mutatingWebhookGVR.Resource + "/":     false,
	}}

	configs, unavailable, denied := collectUpgradeWebhookConfigurations(t.Context(), client, authz)
	if len(configs) != 0 || !slices.Equal(unavailable, []string{"mutatingwebhookconfigurations", "validatingwebhookconfigurations"}) || !slices.Equal(denied, []string{"mutatingwebhookconfigurations"}) {
		t.Fatalf("webhook evidence = configs=%v unavailable=%v denied=%v", configs, unavailable, denied)
	}
}

func TestCollectUpgradeWebhookConfigurationsClassifiesObservedForbidden(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		validatingWebhookGVR: "ValidatingWebhookConfigurationList",
		mutatingWebhookGVR:   "MutatingWebhookConfigurationList",
	})
	client.PrependReactor("list", "validatingwebhookconfigurations", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(validatingWebhookGVR.GroupResource(), "", errors.New("denied"))
	})
	authz := &fakeUpgradeAuthorizer{canList: map[string]bool{
		validatingWebhookGVR.Group + "/" + validatingWebhookGVR.Resource + "/": true,
		mutatingWebhookGVR.Group + "/" + mutatingWebhookGVR.Resource + "/":     true,
	}}

	configs, unavailable, denied := collectUpgradeWebhookConfigurations(t.Context(), client, authz)
	if len(configs) != 0 || !slices.Equal(unavailable, []string{"validatingwebhookconfigurations"}) || !slices.Equal(denied, []string{"validatingwebhookconfigurations"}) {
		t.Fatalf("webhook evidence = configs=%v unavailable=%v denied=%v", configs, unavailable, denied)
	}
}

func TestCollectUpgradeWebhookConfigurationsDoesNotClassifyFailedAuthorizationAsDenial(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		validatingWebhookGVR: "ValidatingWebhookConfigurationList",
		mutatingWebhookGVR:   "MutatingWebhookConfigurationList",
	})
	authz := &fakeUpgradeAuthorizer{listDecisions: map[string]EvidenceAuthorizationDecision{
		validatingWebhookGVR.Group + "/" + validatingWebhookGVR.Resource + "/": {},
		mutatingWebhookGVR.Group + "/" + mutatingWebhookGVR.Resource + "/": {
			Allowed: true, Authoritative: true,
		},
	}}

	configs, unavailable, denied := collectUpgradeWebhookConfigurations(t.Context(), client, authz)
	if len(configs) != 0 || !slices.Equal(unavailable, []string{"validatingwebhookconfigurations"}) || len(denied) != 0 {
		t.Fatalf("webhook evidence = configs=%v unavailable=%v denied=%v, want an unattributed authorization-check failure", configs, unavailable, denied)
	}
}
