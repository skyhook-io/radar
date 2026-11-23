package k8s

import (
	"bytes"
	"log"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
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

func TestMetricsPollWithStalledDiscovery(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		expired bool
		stop    bool
	}{
		{name: "install after miss"},
		{name: "upgrade after expiry", expired: true},
		{name: "stop before refresh returns", stop: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				client := fakeclientset.NewSimpleClientset().Discovery().(*fakediscovery.FakeDiscovery)
				metrics := &metav1.APIResourceList{
					GroupVersion: "metrics.k8s.io/v1",
					APIResources: []metav1.APIResource{
						{Name: "pods", Kind: "PodMetrics", Namespaced: true},
						{Name: "nodes", Kind: "NodeMetrics"},
					},
				}
				if scenario.expired {
					beta := metrics.DeepCopy()
					beta.GroupVersion = "metrics.k8s.io/v1beta1"
					client.Resources = []*metav1.APIResourceList{beta}
				}
				discovery, err := k8score.NewResourceDiscovery(client, k8score.WithDiscoveryCacheTTL(time.Hour))
				if err != nil {
					t.Fatal(err)
				}
				if scenario.expired {
					time.Sleep(time.Hour + time.Second)
				}
				client.Resources = []*metav1.APIResourceList{metrics}
				release := make(chan struct{})
				unblock := sync.OnceFunc(func() { close(release) })
				defer unblock()
				refreshes := 0
				client.PrependReactor("get", "resource", func(clienttesting.Action) (bool, runtime.Object, error) {
					refreshes++
					<-release
					return false, nil, nil
				})

				c, dyn := metricsTestConnection(t, "v1", "node-a")
				c.discovery.Store(&ResourceDiscovery{ResourceDiscovery: discovery})
				c.initMetricsHistory()
				defer c.closeMetricsHistory()
				synctest.Wait()
				if refreshes != 1 {
					t.Fatalf("blocked discovery refreshes = %d, want 1", refreshes)
				}
				time.Sleep(MetricsPollInterval)
				synctest.Wait()
				health := c.MetricsHistory().CollectionHealth()
				if got := health.TotalCollections; got != 2 {
					t.Errorf("poll attempts during blocked refresh = %d, want 2", got)
				}
				if scenario.expired && (health.PodMetrics != (MetricsSourceHealth{}) || health.NodeMetrics != (MetricsSourceHealth{})) {
					t.Errorf("expired discovery changed source health: %+v", health)
				}
				if actions := dyn.Actions(); len(actions) != 0 {
					t.Fatalf("poll issued metrics requests during refresh: %v", actions)
				}
				if scenario.stop {
					var output bytes.Buffer
					previous := log.Writer()
					log.SetOutput(&output)
					c.MetricsHistory().Stop()
					synctest.Wait()
					log.SetOutput(previous)
					if !strings.Contains(output.String(), "Metrics history collection stopped") {
						t.Error("poll loop did not exit before discovery was released")
					}
				}
				unblock()
				synctest.Wait()
				if refreshes != 1 {
					t.Errorf("refreshes for two blocked ticks = %d, want 1", refreshes)
				}
				if gvr, ok := discovery.GetGVRWithGroup("pods", k8score.MetricsAPIGroup); !ok || gvr.Version != "v1" {
					t.Fatalf("refreshed pod metrics = %v, found %v", gvr, ok)
				}
				if !scenario.stop {
					time.Sleep(MetricsPollInterval)
					synctest.Wait()
					if c.MetricsHistory().GetNodeMetricsHistory("node-a") == nil {
						t.Fatal("next tick did not collect the newly served metrics version")
					}
				}
			})
		})
	}
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
