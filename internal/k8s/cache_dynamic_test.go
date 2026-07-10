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

func TestHighChurnDynamicBuiltinsBypassInformer(t *testing.T) {
	cases := []struct {
		name       string
		group      string
		version    string
		kind       string
		resource   string
		listKind   string
		namespace  string
		apiVersion string
	}{
		{
			name:       "EndpointSlice",
			group:      "discovery.k8s.io",
			version:    "v1",
			kind:       "EndpointSlice",
			resource:   "endpointslices",
			listKind:   "EndpointSliceList",
			namespace:  "default",
			apiVersion: "discovery.k8s.io/v1",
		},
		{
			name:       "Lease",
			group:      "coordination.k8s.io",
			version:    "v1",
			kind:       "Lease",
			resource:   "leases",
			listKind:   "LeaseList",
			namespace:  "kube-node-lease",
			apiVersion: "coordination.k8s.io/v1",
		},
		{
			name:       "Endpoints",
			group:      "",
			version:    "v1",
			kind:       "Endpoints",
			resource:   "endpoints",
			listKind:   "EndpointsList",
			namespace:  "default",
			apiVersion: "v1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer ResetTestDynamicState()

			gvr := schema.GroupVersionResource{Group: tc.group, Version: tc.version, Resource: tc.resource}
			lastApplied := `{"kind":"` + tc.kind + `"}`
			obj := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": tc.apiVersion,
				"kind":       tc.kind,
				"metadata": map[string]any{
					"name":      "sample",
					"namespace": tc.namespace,
					"annotations": map[string]any{
						"kubectl.kubernetes.io/last-applied-configuration": lastApplied,
					},
				},
			}}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
				runtime.NewScheme(),
				map[schema.GroupVersionResource]string{gvr: tc.listKind},
				obj,
			)
			if err := InitTestDynamicResourceCache(dyn, []APIResource{{
				Group:      tc.group,
				Version:    tc.version,
				Kind:       tc.kind,
				Name:       tc.resource,
				Namespaced: true,
				Verbs:      []string{"get", "list", "watch"},
			}}); err != nil {
				t.Fatalf("InitTestDynamicResourceCache: %v", err)
			}

			cache := &ResourceCache{}
			items, err := cache.ListDynamicWithGroup(context.Background(), tc.kind, tc.namespace, tc.group)
			if err != nil {
				t.Fatalf("ListDynamicWithGroup: %v", err)
			}
			if len(items) != 1 || items[0].GetName() != "sample" {
				t.Fatalf("ListDynamicWithGroup returned %#v", items)
			}
			got, err := cache.GetDynamicWithGroup(context.Background(), tc.kind, tc.namespace, "sample", tc.group)
			if err != nil {
				t.Fatalf("GetDynamicWithGroup: %v", err)
			}
			if got.GetName() != "sample" {
				t.Fatalf("GetDynamicWithGroup name = %q", got.GetName())
			}
			if _, ok := got.GetAnnotations()["kubectl.kubernetes.io/last-applied-configuration"]; ok {
				t.Fatalf("GetDynamicWithGroup should strip last-applied")
			}
			preserved, err := cache.GetDynamicWithGroupPreserveLastApplied(context.Background(), tc.kind, tc.namespace, "sample", tc.group)
			if err != nil {
				t.Fatalf("GetDynamicWithGroupPreserveLastApplied: %v", err)
			}
			if preserved.GetAnnotations()["kubectl.kubernetes.io/last-applied-configuration"] != lastApplied {
				t.Fatalf("GetDynamicWithGroupPreserveLastApplied did not preserve last-applied: %v", preserved.GetAnnotations())
			}
			if count := GetDynamicResourceCache().GetInformerCount(); count != 0 {
				t.Fatalf("%s started %d dynamic informer(s), want direct reads", tc.kind, count)
			}
		})
	}
}
