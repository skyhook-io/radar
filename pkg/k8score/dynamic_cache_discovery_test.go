package k8score

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestDiscoverAllCRDsDefersDeprecatedCrossplaneUsage(t *testing.T) {
	legacyUsage := schema.GroupVersionResource{
		Group: "apiextensions.crossplane.io", Version: "v1beta1", Resource: "usages",
	}
	currentUsage := schema.GroupVersionResource{
		Group: "protection.crossplane.io", Version: "v1beta1", Resource: "usages",
	}
	discovery := &ResourceDiscovery{
		resources: []APIResource{
			{
				Group: legacyUsage.Group, Version: legacyUsage.Version, Kind: "Usage", Name: legacyUsage.Resource,
				IsCRD: true, Verbs: []string{"get", "list", "watch"},
			},
			{
				Group: currentUsage.Group, Version: currentUsage.Version, Kind: "Usage", Name: currentUsage.Resource,
				IsCRD: true, Verbs: []string{"get", "list", "watch"},
			},
		},
		resourceMap: make(map[string]APIResource),
		gvrMap:      make(map[string]schema.GroupVersionResource),
		lastRefresh: time.Now(),
		cacheTTL:    time.Hour,
	}
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			legacyUsage:  "UsageList",
			currentUsage: "UsageList",
		},
	)
	cache, err := NewDynamicResourceCache(DynamicCacheConfig{
		DynamicClient: dynamicClient,
		Discovery:     discovery,
	})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache failed: %v", err)
	}
	t.Cleanup(cache.Stop)

	cache.DiscoverAllCRDs()
	select {
	case <-cache.discoveryDone:
	case <-time.After(5 * time.Second):
		t.Fatal("DiscoverAllCRDs did not complete")
	}

	if cache.hasCoveringInformer(legacyUsage, "") {
		t.Fatal("deprecated apiextensions.crossplane.io Usage was eagerly watched")
	}
	if !cache.hasCoveringInformer(currentUsage, "") {
		t.Fatal("protection.crossplane.io Usage was not eagerly watched")
	}

	if err := cache.EnsureWatching(legacyUsage); err != nil {
		t.Fatalf("deprecated Usage should remain available on demand: %v", err)
	}
	if !cache.WaitForSync(legacyUsage, 5*time.Second) {
		t.Fatal("deprecated Usage on-demand informer did not sync")
	}
}
