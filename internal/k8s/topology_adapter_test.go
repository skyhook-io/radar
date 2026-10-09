package k8s

import (
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/topology"
)

// newAdapterCache builds a k8s.ResourceCache wrapping a k8score.ResourceCache
// configured for the given ResourceTypes + DeferredTypes. A list reactor can
// be attached via clientMod to simulate API failures.
func newAdapterCache(t *testing.T, enabled, deferred map[string]bool, timeout time.Duration, clientMod func(*fake.Clientset)) *ResourceCache {
	t.Helper()
	client := fake.NewClientset()
	if clientMod != nil {
		clientMod(client)
	}
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:              client,
		ResourceTypes:       enabled,
		DeferredTypes:       deferred,
		DeferredSyncTimeout: timeout,
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	t.Cleanup(core.Stop)
	return &ResourceCache{ResourceCache: core}
}

// TestTopologyAdapter_ConfigMaps_DeferredPending verifies that while the
// ConfigMaps informer is still deferred-pending, the adapter returns
// (nil, nil) instead of a misleading "RBAC not granted" error. This is the
// core fix for issue #460 — one failing sibling must not produce misleading
// topology warnings for healthy-but-not-yet-synced resources.
func TestTopologyAdapter_ConfigMaps_DeferredPending(t *testing.T) {
	// Make ConfigMaps LIST hang long enough to observe the pending state.
	// A reactor that returns no items but takes >100ms keeps HasSynced=false
	// during our observation window.
	cache := newAdapterCache(t,
		map[string]bool{k8score.Pods: true, k8score.ConfigMaps: true},
		map[string]bool{k8score.ConfigMaps: true},
		3*time.Second,
		func(c *fake.Clientset) {
			c.PrependReactor("list", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
				time.Sleep(200 * time.Millisecond)
				return true, &corev1.ConfigMapList{}, nil
			})
		},
	)

	// Observe while pending: IsDeferredPending must be true, lister must be nil.
	if !cache.IsDeferredPending(k8score.ConfigMaps) {
		t.Fatal("expected ConfigMaps to be deferred-pending immediately after start")
	}
	if cache.ConfigMaps() != nil {
		t.Fatal("expected nil lister during deferred-pending")
	}

	adapter := NewTopologyResourceProvider(cache)
	cms, err := adapter.ConfigMaps()
	if err != nil {
		t.Errorf("expected no error during deferred-pending, got: %v", err)
	}
	if cms != nil {
		t.Errorf("expected nil slice during deferred-pending, got %d items", len(cms))
	}
}

// TestTopologyAdapter_ConfigMaps_RBACDenied verifies that when the informer
// was never enabled (simulating RBAC denial), the adapter returns the genuine
// "RBAC not granted" error — not the silent nil,nil path used for pending.
func TestTopologyAdapter_ConfigMaps_RBACDenied(t *testing.T) {
	cache := newAdapterCache(t,
		map[string]bool{k8score.Pods: true}, // ConfigMaps deliberately NOT enabled
		map[string]bool{},
		3*time.Second,
		nil,
	)

	if cache.IsDeferredPending(k8score.ConfigMaps) {
		t.Fatal("ConfigMaps was never enabled; IsDeferredPending must be false")
	}
	if cache.ConfigMaps() != nil {
		t.Fatal("expected nil lister for unenabled resource")
	}

	adapter := NewTopologyResourceProvider(cache)
	cms, err := adapter.ConfigMaps()
	if err == nil {
		t.Error("expected RBAC error for unenabled resource, got nil")
	}
	if cms != nil {
		t.Errorf("expected nil slice on RBAC denial, got %d items", len(cms))
	}
}

// TestTopologyAdapter_ConfigMaps_Synced verifies the happy path: when the
// informer is synced, the adapter returns the list with no error.
func TestTopologyAdapter_ConfigMaps_Synced(t *testing.T) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cm-1", Namespace: "default"}}
	client := fake.NewClientset(cm)
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:        client,
		ResourceTypes: map[string]bool{k8score.Pods: true, k8score.ConfigMaps: true},
		DeferredTypes: map[string]bool{k8score.ConfigMaps: true},
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	t.Cleanup(core.Stop)
	cache := &ResourceCache{ResourceCache: core}

	// Wait for ConfigMaps to become ready (fake client syncs fast).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cache.ConfigMaps() != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if cache.ConfigMaps() == nil {
		t.Fatal("ConfigMaps never became ready")
	}

	adapter := NewTopologyResourceProvider(cache)
	cms, err := adapter.ConfigMaps()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(cms) != 1 {
		t.Errorf("expected 1 ConfigMap, got %d", len(cms))
	}
}

// TestTopologyAdapter_NetworkPolicies_DeferredPending verifies that the same
// (nil, nil) behavior applies to NetworkPolicies — the guard was added to
// six methods, and at least one other should be exercised so a regression
// that only fixes ConfigMaps doesn't pass.
func TestTopologyAdapter_NetworkPolicies_DeferredPending(t *testing.T) {
	cache := newAdapterCache(t,
		map[string]bool{k8score.Pods: true, k8score.NetworkPolicies: true},
		map[string]bool{k8score.NetworkPolicies: true},
		3*time.Second,
		func(c *fake.Clientset) {
			c.PrependReactor("list", "networkpolicies", func(action k8stesting.Action) (bool, runtime.Object, error) {
				time.Sleep(200 * time.Millisecond)
				return true, nil, fmt.Errorf("slow api")
			})
		},
	)

	if !cache.IsDeferredPending(k8score.NetworkPolicies) {
		t.Fatal("expected NetworkPolicies to be deferred-pending immediately after start")
	}

	adapter := NewTopologyResourceProvider(cache)
	nps, err := adapter.NetworkPolicies()
	if err != nil {
		t.Errorf("expected no error during deferred-pending, got: %v", err)
	}
	if nps != nil {
		t.Errorf("expected nil slice during deferred-pending, got %d items", len(nps))
	}
}

func TestTopologyAdapter_ForNamespaceUsesCachedIndex(t *testing.T) {
	client := fake.NewClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "one", Namespace: "a"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "two", Namespace: "b"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"}},
	)
	core, err := k8score.NewResourceCache(k8score.CacheConfig{Client: client, ResourceTypes: map[string]bool{k8score.Pods: true, k8score.Nodes: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	cache := &ResourceCache{ResourceCache: core}
	adapter := NewTopologyResourceProvider(cache)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pods, _ := adapter.Pods()
		nodes, _ := adapter.Nodes()
		if len(pods) == 2 && len(nodes) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	client.ClearActions()
	scoped := adapter.(topology.NamespacedResourceProvider).ForNamespace("a")
	pods, err := scoped.Pods()
	if err != nil || len(pods) != 1 || pods[0].Namespace != "a" {
		t.Fatalf("scoped Pods: %v %v", pods, err)
	}
	nodes, err := scoped.Nodes()
	if err != nil || len(nodes) != 1 {
		t.Fatalf("cluster list narrowed: %v %v", nodes, err)
	}
	pods, err = adapter.Pods()
	if err != nil || len(pods) != 2 {
		t.Fatalf("shared adapter mutated: %v %v", pods, err)
	}
	pods, err = adapter.(topology.NamespacedResourceProvider).ForNamespace("").Pods()
	if err != nil || len(pods) != 2 {
		t.Fatalf("empty scope: %v %v", pods, err)
	}
	pods, err = adapter.(topology.NamespacedResourceProvider).ForNamespace("unobserved").Pods()
	if err != nil || len(pods) != 0 {
		t.Fatalf("unobserved scope: %v %v", pods, err)
	}
	if actions := client.Actions(); len(actions) != 0 {
		t.Fatalf("namespace projection made API calls: %v", actions)
	}
	if _, err := scoped.ReplicaSets(); err == nil {
		t.Fatal("disabled informer should retain its availability error")
	}
}
