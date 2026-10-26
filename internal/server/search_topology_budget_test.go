package server

import (
	"net/http"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

// The managedBy enrichment needs the topology, whose cold build reads custom
// resources; right after a restart those reads wait on custom-resource
// discovery for tens of seconds. Search answers within its budget instead.
func TestSearchDoesNotWaitOnAColdTopologyBuild(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "robusta-config", Namespace: "default"}},
	))
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
	env := newAuthTestServer(t)
	env.srv.permCache.Set("search-cold", nil, &auth.UserPermissions{})
	env.srv.broadcaster.markCachedTopologyDirty()
	time.AfterFunc(20*time.Second, unblock)

	start := time.Now()
	resp := env.authGet(t, "/api/search?q=robusta&globalNs=1", "search-cold", "")
	resp.Body.Close()
	if elapsed := time.Since(start); resp.StatusCode != http.StatusOK || elapsed > 8*time.Second {
		t.Fatalf("search took %s (status %d) while the topology was still building", elapsed, resp.StatusCode)
	}
}
