package server

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestCollectUpgradeSourceObjectsRetainsPartialEvidence(t *testing.T) {
	listKinds := make(map[schema.GroupVersionResource]string, len(upgradeSourceGVRs))
	for _, gvr := range upgradeSourceGVRs {
		listKinds[gvr] = "UpgradeSourceList"
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

	objects, unavailable := collectUpgradeSourceObjectsWithClient(context.Background(), client, nil)
	if len(objects) != 4 {
		t.Fatalf("partial objects = %#v, want Pod, Ingress, HPA, and PDB", objects)
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

func TestCollectUpgradeSourceObjectsReturnsNilWhenEveryListFails(t *testing.T) {
	listKinds := make(map[schema.GroupVersionResource]string, len(upgradeSourceGVRs))
	for _, gvr := range upgradeSourceGVRs {
		listKinds[gvr] = "UpgradeSourceList"
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	client.PrependReactor("list", "*", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("unavailable")
	})

	objects, unavailable := collectUpgradeSourceObjectsWithClient(context.Background(), client, []string{"default"})
	if objects != nil {
		t.Fatalf("objects = %#v, want nil when every list fails", objects)
	}
	if len(unavailable) != len(upgradeSourceGVRs) {
		t.Fatalf("unavailable = %v, want %d kinds", unavailable, len(upgradeSourceGVRs))
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
