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
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, pod)
	client.PrependReactor("list", "networkpolicies", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden")
	})

	objects, unavailable := collectUpgradeSourceObjectsWithClient(context.Background(), client, nil)
	if len(objects) != 1 || objects[0].GetName() != "api" {
		t.Fatalf("partial objects = %#v, want retained Pod", objects)
	}
	if len(unavailable) != 1 || unavailable[0] != "networkpolicies" {
		t.Fatalf("unavailable = %v, want [networkpolicies]", unavailable)
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
