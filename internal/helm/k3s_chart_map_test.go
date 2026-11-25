package helm

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// k3sHelmChartMap reads HelmCharts and HelmChartConfigs straight from the
// dynamic cache.
func TestK3sHelmChartMapReadsTheCache(t *testing.T) {
	useCrossNamespaceCache(t)
	charts := schema.GroupVersionResource{Group: k3sHelmGroup, Version: "v1", Resource: "helmcharts"}
	configs := schema.GroupVersionResource{Group: k3sHelmGroup, Version: "v1", Resource: "helmchartconfigs"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{charts: "HelmChartList", configs: "HelmChartConfigList"},
		k3sObj("HelmChart", "kube-system", "app", map[string]any{"chartContent": "H4sI", "targetNamespace": "apps"}),
		k3sObj("HelmChartConfig", "kube-system", "app", nil),
	)
	verbs := []string{"get", "list", "watch"}
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
		{Group: k3sHelmGroup, Version: "v1", Name: "helmcharts", Kind: "HelmChart", Namespaced: true, Verbs: verbs},
		{Group: k3sHelmGroup, Version: "v1", Name: "helmchartconfigs", Kind: "HelmChartConfig", Namespaced: true, Verbs: verbs},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)

	dc := k8s.GetDynamicResourceCache()
	for _, gvr := range []schema.GroupVersionResource{charts, configs} {
		if err := dc.EnsureWatching(gvr); err != nil || !dc.WaitForSync(gvr, 5*time.Second) {
			t.Fatalf("%v did not sync: %v", gvr, err)
		}
	}
	got := k3sHelmChartMap(context.Background(), k8s.Current())
	if owner := got["apps/app"]; owner.chart != "kube-system/app" || owner.config != "kube-system/app" || !owner.configKnown {
		t.Fatalf("map = %+v", got)
	}
}

// A HelmChartConfig informer that hasn't synced yet holds nothing: the config
// must read as unknown, not as missing, or the note says to create one that
// exists.
func TestK3sHelmChartMapLeavesConfigUnknownUntilItsCacheSyncs(t *testing.T) {
	useCrossNamespaceCache(t)
	charts := schema.GroupVersionResource{Group: k3sHelmGroup, Version: "v1", Resource: "helmcharts"}
	configs := schema.GroupVersionResource{Group: k3sHelmGroup, Version: "v1", Resource: "helmchartconfigs"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{charts: "HelmChartList", configs: "HelmChartConfigList"},
		k3sObj("HelmChart", "kube-system", "app", map[string]any{"chart": "app", "targetNamespace": "apps"}),
		k3sObj("HelmChartConfig", "kube-system", "app", nil),
	)
	// The first list is the cache's access probe; the informer's own list
	// after it is held back, so the informer stays unsynced.
	release := make(chan struct{})
	var lists atomic.Int32
	dyn.PrependReactor("list", "helmchartconfigs", func(k8stesting.Action) (bool, runtime.Object, error) {
		if lists.Add(1) > 1 {
			<-release
		}
		return false, nil, nil
	})
	verbs := []string{"get", "list", "watch"}
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
		{Group: k3sHelmGroup, Version: "v1", Name: "helmcharts", Kind: "HelmChart", Namespaced: true, Verbs: verbs},
		{Group: k3sHelmGroup, Version: "v1", Name: "helmchartconfigs", Kind: "HelmChartConfig", Namespaced: true, Verbs: verbs},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)

	dc := k8s.GetDynamicResourceCache()
	if err := dc.EnsureWatching(charts); err != nil || !dc.WaitForSync(charts, 5*time.Second) {
		t.Fatalf("HelmCharts did not sync: %v", err)
	}
	owner := k3sHelmChartMap(context.Background(), k8s.Current())["apps/app"]
	if owner.chart != "kube-system/app" || owner.configExists() != nil {
		t.Fatalf("before the config cache syncs: owner = %+v, exists = %v", owner, owner.configExists())
	}

	unblock()
	if !dc.WaitForSync(configs, 5*time.Second) {
		t.Fatal("HelmChartConfigs did not sync")
	}
	owner = k3sHelmChartMap(context.Background(), k8s.Current())["apps/app"]
	if exists := owner.configExists(); exists == nil || !*exists {
		t.Fatalf("after sync: owner = %+v", owner)
	}
}
