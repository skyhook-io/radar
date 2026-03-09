//go:build e2e

package k8s

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"

	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/topology"
)

// skipIfNoCluster skips the test if no kubeconfig is available or the cluster
// is unreachable.
func skipIfNoCluster(t *testing.T) {
	t.Helper()
	config, err := clientcmd.BuildConfigFromFlags("", resolveKubeconfig())
	if err != nil {
		t.Skipf("skipping e2e: cannot build kubeconfig: %v", err)
	}
	config.Timeout = 5 * time.Second
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Skipf("skipping e2e: cannot create client: %v", err)
	}
	_, err = client.Discovery().ServerVersion()
	if err != nil {
		t.Skipf("skipping e2e: cluster unreachable: %v", err)
	}
}

func resolveKubeconfig() string {
	if p := os.Getenv("KUBECONFIG"); p != "" {
		return p
	}
	if home := homedir.HomeDir(); home != "" {
		return filepath.Join(home, ".kube", "config")
	}
	return ""
}

// TestE2EStartupAndSync tests the full cache startup sequence against a real cluster.
func TestE2EStartupAndSync(t *testing.T) {
	skipIfNoCluster(t)
	defer ResetTestState()

	timeline.InitStore(timeline.DefaultStoreConfig())
	defer timeline.ResetStore()

	// Initialize using the real path (RBAC checks, informer creation)
	err := Initialize(InitOptions{KubeconfigPath: resolveKubeconfig()})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	err = InitResourceCache(ctx)
	if err != nil {
		t.Fatalf("InitResourceCache: %v", err)
	}

	cache := GetResourceCache()
	if cache == nil {
		t.Fatal("cache is nil after initialization")
	}

	// Critical sync should have completed (blocked during InitResourceCache)
	if !cache.IsSyncComplete() {
		t.Error("critical sync should be complete after InitResourceCache returns")
	}

	// Wait for deferred sync with timeout
	select {
	case <-cache.DeferredDone():
		if !cache.IsDeferredSynced() {
			t.Error("deferred sync channel closed but IsDeferredSynced() is false")
		}
		t.Log("deferred sync complete")
	case <-time.After(2 * time.Minute):
		t.Error("deferred sync did not complete within 2 minutes")
	}

	// Log resource counts for visibility
	logResourceCount(t, "Pods", func() (int, error) {
		if l := cache.Pods(); l != nil {
			items, err := l.List(labels.Everything())
			return len(items), err
		}
		return 0, nil
	})
	logResourceCount(t, "Deployments", func() (int, error) {
		if l := cache.Deployments(); l != nil {
			items, err := l.List(labels.Everything())
			return len(items), err
		}
		return 0, nil
	})
	logResourceCount(t, "Services", func() (int, error) {
		if l := cache.Services(); l != nil {
			items, err := l.List(labels.Everything())
			return len(items), err
		}
		return 0, nil
	})
	logResourceCount(t, "Namespaces", func() (int, error) {
		if l := cache.Namespaces(); l != nil {
			items, err := l.List(labels.Everything())
			return len(items), err
		}
		return 0, nil
	})
}

func logResourceCount(t *testing.T, kind string, countFn func() (int, error)) {
	t.Helper()
	count, err := countFn()
	if err != nil {
		t.Logf("  %s: error listing: %v", kind, err)
		return
	}
	t.Logf("  %s: %d", kind, count)
}

// TestE2ETopologyBuild tests topology construction against a real cluster.
func TestE2ETopologyBuild(t *testing.T) {
	skipIfNoCluster(t)
	defer ResetTestState()

	timeline.InitStore(timeline.DefaultStoreConfig())
	defer timeline.ResetStore()

	if err := Initialize(InitOptions{KubeconfigPath: resolveKubeconfig()}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := InitResourceCache(ctx); err != nil {
		t.Fatalf("InitResourceCache: %v", err)
	}

	// Wait for deferred sync so topology has full data
	select {
	case <-GetResourceCache().DeferredDone():
	case <-time.After(2 * time.Minute):
		t.Fatal("deferred sync timeout")
	}

	provider := NewTopologyResourceProvider(GetResourceCache())
	builder := topology.NewBuilder(provider)

	t.Run("default all-namespace build", func(t *testing.T) {
		opts := topology.DefaultBuildOptions()
		topo, err := builder.Build(opts)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		t.Logf("topology: %d nodes, %d edges, largeCluster=%v, requiresNamespaceFilter=%v",
			len(topo.Nodes), len(topo.Edges), topo.LargeCluster, topo.RequiresNamespaceFilter)

		if topo.RequiresNamespaceFilter {
			// Large cluster — nodes/edges should be empty
			if len(topo.Nodes) != 0 {
				t.Errorf("requiresNamespaceFilter but got %d nodes", len(topo.Nodes))
			}
		} else {
			// Small/medium cluster — should have nodes
			if len(topo.Nodes) == 0 {
				t.Error("expected at least some nodes from a connected cluster")
			}
		}
	})

	t.Run("kube-system namespace filter", func(t *testing.T) {
		opts := topology.DefaultBuildOptions()
		opts.Namespaces = []string{"kube-system"}
		topo, err := builder.Build(opts)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		t.Logf("kube-system: %d nodes, %d edges", len(topo.Nodes), len(topo.Edges))

		if topo.RequiresNamespaceFilter {
			t.Error("namespace-filtered build should not require namespace filter")
		}
		if len(topo.Nodes) == 0 {
			t.Error("kube-system should have at least some resources")
		}
	})

	t.Run("ForRelationshipCache bypasses large cluster guard", func(t *testing.T) {
		opts := topology.DefaultBuildOptions()
		opts.ForRelationshipCache = true
		opts.IncludeReplicaSets = true

		topo, err := builder.Build(opts)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		t.Logf("relationship cache: %d nodes, %d edges", len(topo.Nodes), len(topo.Edges))

		// Regardless of cluster size, ForRelationshipCache must produce nodes
		if len(topo.Nodes) == 0 {
			t.Error("ForRelationshipCache build should always produce nodes")
		}
		if topo.RequiresNamespaceFilter {
			t.Error("ForRelationshipCache should never set RequiresNamespaceFilter")
		}
	})
}

