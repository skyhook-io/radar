package k8s

import (
	"context"
	"reflect"
	"testing"
	"time"

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
			borrowed, err := cache.ListDynamicReadOnlyWithGroup(context.Background(), tc.kind, tc.namespace, tc.group)
			if err != nil || !reflect.DeepEqual(items, borrowed) {
				t.Fatalf("read-only direct list=%v err=%v, want %v", borrowed, err, items)
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

func TestListDynamicReadOnlyWithGroupStartsControllerRevisionWatch(t *testing.T) {
	defer ResetTestDynamicState()
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "controllerrevisions"}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "ControllerRevision",
		"metadata": map[string]any{"namespace": "prod", "name": "revision", "managedFields": []any{map[string]any{"manager": "controller"}}, "annotations": map[string]any{"kubectl.kubernetes.io/last-applied-configuration": "{}", "keep": "yes"}},
		"revision": int64(1),
	}}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "ControllerRevisionList"}, obj)
	if err := InitTestDynamicResourceCache(client, []APIResource{{Group: "apps", Version: "v1", Kind: "ControllerRevision", Name: "controllerrevisions", Namespaced: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	cache := &ResourceCache{}
	dynamic := GetDynamicResourceCache()
	if dynamic.GetInformerCount() != 0 {
		t.Fatal("watch started before the read")
	}
	if _, err := cache.ListDynamicReadOnlyWithGroup(context.Background(), "ControllerRevision", "prod", "apps"); err != nil {
		t.Fatal(err)
	}
	if !dynamic.WaitForSync(gvr, 2*time.Second) {
		t.Fatal("ControllerRevision watch did not sync")
	}
	first, err := cache.ListDynamicReadOnlyWithGroup(context.Background(), "ControllerRevision", "prod", "apps")
	if err != nil || len(first) != 1 {
		t.Fatalf("read=%v err=%v", first, err)
	}
	second, err := cache.ListDynamicReadOnlyWithGroup(context.Background(), "ControllerRevision", "prod", "apps")
	if err != nil || len(second) != 1 || first[0] != second[0] {
		t.Fatal("read-only list did not borrow the cached object")
	}
	copied, err := cache.ListDynamicWithGroup(context.Background(), "ControllerRevision", "prod", "apps")
	if err != nil || len(copied) != 1 || first[0] == copied[0] || !reflect.DeepEqual(first[0], copied[0]) {
		t.Fatal("copying and read-only lists differ in value or ownership")
	}
	if first[0].GetAnnotations()["keep"] != "yes" {
		t.Fatal("read-only list stripped a retained annotation")
	}
	if len(first[0].GetManagedFields()) != 0 || first[0].GetAnnotations()["kubectl.kubernetes.io/last-applied-configuration"] != "" {
		t.Fatal("borrowed object was not stripped")
	}
}
