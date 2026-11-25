package k8s

import (
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

func metricsTestConnection(t *testing.T, version, node string) (*Connection, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	d := buildFakeDiscoveryCore(t)
	kinds := map[schema.GroupVersionResource]string{}
	for _, r := range []APIResource{
		{Group: k8score.MetricsAPIGroup, Version: version, Name: "pods", Kind: "PodMetrics", Namespaced: true},
		{Group: k8score.MetricsAPIGroup, Version: version, Name: "nodes", Kind: "NodeMetrics"},
	} {
		d.AddAPIResource(r)
		kinds[schema.GroupVersionResource{Group: r.Group, Version: r.Version, Resource: r.Name}] = r.Kind + "List"
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), kinds)
	dyn.PrependReactor("list", "nodes", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{{Object: map[string]any{
			"metadata": map[string]any{"name": node},
			"usage":    map[string]any{"cpu": "25m", "memory": "1Mi"},
		}}}}, nil
	})
	c := &Connection{clients: &sharedKubernetesClients{dynamic: dyn}}
	c.publishDiscovery(d)
	return c, dyn
}

func TestMetricsHistoryInFlightAcrossClose(t *testing.T) {
	withFreshTestConnection(t)
	synctest.Test(t, func(t *testing.T) {
		a, dyn := metricsTestConnection(t, "v1beta1", "node-a")
		b, _ := metricsTestConnection(t, "v1", "node-b")
		release := make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer a.closeMetricsHistory()
		defer unblock()
		dyn.PrependReactor("list", "pods", func(clienttesting.Action) (bool, runtime.Object, error) {
			<-release
			return true, &unstructured.UnstructuredList{}, nil
		})
		restoreTestConnection(a)
		InitMetricsHistory()
		synctest.Wait()
		old := a.MetricsHistory()
		if old == nil || old.CollectionHealth().TotalCollections != 1 {
			t.Fatal("A did not start its poll")
		}
		if a.fresh().MetricsHistory() != nil {
			t.Fatal("a rebuild on the same clients inherited the old history")
		}
		go ResetMetricsHistory()
		synctest.Wait()

		restoreTestConnection(b)
		if GetMetricsHistory() != nil {
			t.Fatal("new connection inherited A's history")
		}
		InitMetricsHistory()
		defer b.closeMetricsHistory()
		synctest.Wait()
		unblock()
		synctest.Wait()
		if a.MetricsHistory() != nil {
			t.Fatal("A still serves a store after close")
		}
		a.initMetricsHistory()
		if a.MetricsHistory() != nil {
			t.Fatal("a late init restarted the closed poller")
		}
		if store := GetMetricsHistory(); store == old || store.GetNodeMetricsHistory("node-a") != nil || store.GetNodeMetricsHistory("node-b") == nil {
			t.Fatal("B must serve only its own samples")
		}
		for _, action := range dyn.Actions() {
			if action.GetResource().Version != "v1beta1" {
				t.Fatalf("A's poll used another connection's discovery: %v", action.GetResource())
			}
		}
		closed := old.CollectionHealth()
		time.Sleep(MetricsPollInterval)
		synctest.Wait()
		if old.CollectionHealth() != closed {
			t.Fatal("the poller continued recording after close returned")
		}
	})
}

func TestReadmissionKeepsMetricsHistory(t *testing.T) {
	t.Cleanup(SnapshotTestConnection())
	clearTestConnectionIdentity()
	synctest.Test(t, func(t *testing.T) {
		c, _ := metricsTestConnection(t, "v1", "node-a")
		restoreTestConnection(c)
		InitMetricsHistory()
		defer c.closeMetricsHistory()
		synctest.Wait()
		store := GetMetricsHistory()
		SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
		first := ServingConnectionID()
		RetireServingConnection()
		SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
		InitMetricsHistory()

		if id := ServingConnectionID(); id == "" || id == first {
			t.Fatalf("re-admission IDs %q then %q, want a new one", first, id)
		}
		history := GetMetricsHistory().GetNodeMetricsHistory("node-a")
		if GetMetricsHistory() != store || history == nil || len(history.DataPoints) != 1 || history.DataPoints[0].CPU != 25_000_000 {
			t.Fatalf("re-admission lost the original sample: %+v", history)
		}
	})
}
