package audit

import (
	"context"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// The audit runs on every AI resource context and scans every watched custom
// kind for Crossplane shapes. It must read the cache's objects in place: a
// copy per object per run made large reports (Trivy's) dominate CPU.
func TestCrossplaneScanReadsCachedObjectsInPlace(t *testing.T) {
	mrGVR := gvr("s3.aws.upbound.io", "v1beta1", "buckets")
	reportGVR := gvr("aquasecurity.github.io", "v1alpha1", "vulnerabilityreports")
	bucket := testUnstructured("s3.aws.upbound.io/v1beta1", "Bucket", "", "logs", map[string]any{
		"spec": map[string]any{"providerConfigRef": map[string]any{"name": "default"}},
	})
	report := testUnstructured("aquasecurity.github.io/v1alpha1", "VulnerabilityReport", "apps", "web", map[string]any{
		"report": map[string]any{"vulnerabilities": []any{map[string]any{"vulnerabilityID": "CVE-1"}}},
	})
	seedDynamic(t, map[schema.GroupVersionResource]string{mrGVR: "BucketList", reportGVR: "VulnerabilityReportList"},
		[]k8s.APIResource{
			{Group: mrGVR.Group, Version: mrGVR.Version, Kind: "Bucket", Name: mrGVR.Resource, Verbs: []string{"list", "watch"}},
			{Group: reportGVR.Group, Version: reportGVR.Version, Kind: "VulnerabilityReport", Name: reportGVR.Resource, Namespaced: true, Verbs: []string{"list", "watch"}},
		},
		map[schema.GroupVersionResource][]*unstructured.Unstructured{mrGVR: {bucket}, reportGVR: {report}})

	cached, err := k8s.GetDynamicResourceCache().ListWatchedReadOnly(mrGVR)
	if err != nil || len(cached) != 1 {
		t.Fatalf("cached buckets: %v %v", cached, err)
	}
	mrs, xrs := listCrossplaneDynamic(nil, nil)
	if len(mrs) != 1 || len(xrs) != 0 {
		t.Fatalf("got %d managed and %d composite resources; want 1 and 0", len(mrs), len(xrs))
	}
	if mrs[0] != cached[0] {
		t.Fatal("the Crossplane scan copied a cached object instead of reading it in place")
	}
}

func seedDynamic(t *testing.T, listKinds map[schema.GroupVersionResource]string, resources []k8s.APIResource, objects map[schema.GroupVersionResource][]*unstructured.Unstructured) {
	t.Helper()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	for g, items := range objects {
		for _, item := range items {
			if _, err := client.Resource(g).Namespace(item.GetNamespace()).Create(context.Background(), item, metav1.CreateOptions{}); err != nil {
				t.Fatalf("create %s fixture: %v", g.Resource, err)
			}
		}
	}
	if err := k8s.InitTestDynamicResourceCache(client, resources); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	cache := k8s.GetDynamicResourceCache()
	for g := range listKinds {
		if err := cache.EnsureWatching(g); err != nil {
			t.Fatalf("EnsureWatching %s: %v", g.Resource, err)
		}
		if !cache.WaitForSync(g, 2*time.Second) {
			t.Fatalf("%s did not sync", g.Resource)
		}
	}
}
