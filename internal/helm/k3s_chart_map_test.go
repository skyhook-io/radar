package helm

import (
	"context"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
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
	got := k3sHelmChartMap(context.Background())
	if owner := got["apps/app"]; owner.chart != "kube-system/app" || owner.config != "kube-system/app" || !owner.configKnown {
		t.Fatalf("map = %+v", got)
	}
}
