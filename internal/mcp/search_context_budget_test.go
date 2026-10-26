package mcp

import (
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
)

// A cold topology build behind the per-hit context reads custom resources,
// which right after a restart wait on custom-resource discovery; the search
// tool answers within its budget instead.
func TestHandleSearchDoesNotWaitOnAColdTopologyBuild(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	apps := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{apps: "ApplicationList"})
	dyn.PrependReactor("list", "applications", func(ktesting.Action) (bool, runtime.Object, error) {
		<-release
		return false, nil, nil
	})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: apps.Group, Version: apps.Version, Name: apps.Resource, Kind: "Application", Namespaced: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	time.AfterFunc(20*time.Second, unblock)

	start := time.Now()
	if _, _, err := handleSearch(withClusterAdmin(t, "search-cold"), nil, searchInput{Query: "pod"}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Fatalf("search took %s while the topology was still building", elapsed)
	}
	t.Cleanup(func() { getPermCache().Invalidate() })
}
