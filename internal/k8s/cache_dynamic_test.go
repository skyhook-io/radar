package k8s

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestGetDynamicWithGroupDirectFetchesAPIService(t *testing.T) {
	defer ResetTestDynamicState()

	gvr := schema.GroupVersionResource{Group: "apiregistration.k8s.io", Version: "v1", Resource: "apiservices"}
	apiService := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiregistration.k8s.io/v1",
		"kind":       "APIService",
		"metadata": map[string]any{
			"name": "v1beta1.metrics.k8s.io",
		},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "APIServiceList"},
		apiService,
	)
	if err := InitTestDynamicResourceCache(dyn, []APIResource{{
		Group:      "apiregistration.k8s.io",
		Version:    "v1",
		Kind:       "APIService",
		Name:       "apiservices",
		Namespaced: false,
	}}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}

	got, err := (&ResourceCache{}).GetDynamicWithGroup(context.Background(), "APIService", "", "v1beta1.metrics.k8s.io", "apiregistration.k8s.io")
	if err != nil {
		t.Fatalf("GetDynamicWithGroup: %v", err)
	}
	if got.GetName() != "v1beta1.metrics.k8s.io" {
		t.Fatalf("GetDynamicWithGroup name = %q", got.GetName())
	}
	if count := GetDynamicResourceCache().GetInformerCount(); count != 0 {
		t.Fatalf("GetDynamicWithGroup(APIService) started %d dynamic informer(s), want direct GET", count)
	}
}
