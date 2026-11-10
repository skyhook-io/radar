package cnpg

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

func TestCollectorReadinessReasonsUseActualCacheState(t *testing.T) {
	for _, tt := range []struct {
		name                 string
		readiness            k8score.KindReadiness
		namespace            string
		reason, configReason string
	}{
		{"ready", k8score.KindReady, "", "", "not found"},
		{"pending", k8score.KindPending, "", "still loading", "still loading"},
		{"failed", k8score.KindFailed, "", "failed to load", "failed to load"},
		{"disabled", k8score.KindUnavailable, "", "not collecting", "not collecting"},
		{"outside scope", k8score.KindReady, "other", "watches", "does not watch"},
		{"pending outside scope", k8score.KindPending, "other", "watches", "does not watch"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			scopes := map[string]k8score.ResourceScope{"services": {Enabled: true}}
			deferred := map[string]bool{}
			delays := map[string]time.Duration{}
			for _, resource := range []string{"configmaps", "persistentvolumeclaims", "poddisruptionbudgets"} {
				scopes[resource] = k8score.ResourceScope{Enabled: tt.readiness != k8score.KindUnavailable, Namespace: tt.namespace}
				if tt.readiness == k8score.KindPending || tt.readiness == k8score.KindFailed {
					deferred[resource] = true
					delays[resource] = time.Hour
				}
			}
			timeout := time.Minute
			if tt.readiness == k8score.KindFailed {
				timeout = time.Millisecond
			}
			core, err := k8score.NewResourceCache(k8score.CacheConfig{Client: fake.NewClientset(), ResourceScopes: scopes, DeferredTypes: deferred, DebugSyncDelays: delays, DeferredSyncTimeout: timeout})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(core.Stop)
			deadline := time.Now().Add(5 * time.Second)
			for core.KindReadinessFor("configmaps") != tt.readiness && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if got := core.KindReadinessFor("configmaps"); got != tt.readiness {
				t.Fatalf("readiness=%v", got)
			}
			cache := &k8s.ResourceCache{ResourceCache: core}
			reader := newTestReader(nil)
			ctx := context.Background()
			config := reader.operatorConfigMap(ctx, cache, "db", "config", "operator")
			if !strings.Contains(config.Reason, tt.configReason) || config.Readable {
				t.Fatalf("config=%+v", config)
			}
			cluster := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "postgresql.cnpg.io/v1", "kind": "Cluster", "metadata": map[string]any{"name": "pg", "namespace": "db"}}}
			pdb := reader.hAPDBs(ctx, cache, cluster)
			claims, reason := cnpgCachedClaims(cache, "db", labels.Everything())
			if tt.reason == "" {
				if pdb.ReadSource.State != cnpgHAStateOK || reason != "" || config.Exists == nil || *config.Exists {
					t.Fatalf("ready empty: pdb=%+v claims=%v reason=%q config=%+v", pdb, claims, reason, config)
				}
			} else {
				if pdb.ReadSource.State != cnpgHAStateUnavailable || !strings.Contains(pdb.ReadSource.Reason, tt.reason) || !strings.Contains(reason, tt.reason) || config.Exists != nil {
					t.Fatalf("unread: pdb=%+v claims=%v reason=%q config=%+v", pdb, claims, reason, config)
				}
			}
			if len(pdb.Items) != 0 || len(claims) != 0 {
				t.Fatal("unexpected objects in empty fixture")
			}
		})
	}
}

func TestRuntimePodInventoriesRequireEverySourceToBeReadable(t *testing.T) {
	object := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"namespace": "db", "name": "pg"}}}
	for _, resource := range []string{"pods", "replicasets", "deployments"} {
		for _, state := range []struct {
			name              string
			readiness         k8score.KindReadiness
			namespace, reason string
		}{
			{"ready empty", k8score.KindReady, "db", ""},
			{"pending", k8score.KindPending, "db", "still loading"},
			{"failed", k8score.KindFailed, "db", "failed to load"},
			{"disabled", k8score.KindUnavailable, "db", "not collecting"},
			{"outside scope", k8score.KindReady, "other", "watches"},
			{"pending outside scope", k8score.KindPending, "other", "watches"},
		} {
			t.Run(resource+"/"+state.name, func(t *testing.T) {
				scopes := map[string]k8score.ResourceScope{"services": {Enabled: true}, "pods": {Enabled: true, Namespace: "db"}, "replicasets": {Enabled: true, Namespace: "db"}, "deployments": {Enabled: true, Namespace: "db"}}
				scopes[resource] = k8score.ResourceScope{Enabled: state.readiness != k8score.KindUnavailable, Namespace: state.namespace}
				deferred, delays := map[string]bool{}, map[string]time.Duration{}
				timeout := time.Minute
				if state.readiness == k8score.KindPending || state.readiness == k8score.KindFailed {
					deferred[resource], delays[resource] = true, time.Hour
					if state.readiness == k8score.KindFailed {
						timeout = time.Millisecond
					}
				}
				core, err := k8score.NewResourceCache(k8score.CacheConfig{Client: fake.NewClientset(), ResourceScopes: scopes, DeferredTypes: deferred, DebugSyncDelays: delays, DeferredSyncTimeout: timeout})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(core.Stop)
				deadline := time.Now().Add(5 * time.Second)
				for core.KindReadinessFor(resource) != state.readiness && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if core.KindReadinessFor(resource) != state.readiness {
					t.Fatal("fixture did not reach requested readiness")
				}
				cache := &k8s.ResourceCache{ResourceCache: core}
				readers := []func(*k8s.ResourceCache, *unstructured.Unstructured) ([]*corev1.Pod, error){poolerPods}
				if resource == "pods" {
					readers = append(readers, clusterInstancePods)
				}
				for _, read := range readers {
					pods, err := read(cache, object)
					if state.reason == "" {
						if err != nil || pods == nil || len(pods) != 0 {
							t.Fatalf("confirmed empty inventory=%v, %v", pods, err)
						}
					} else if err == nil || !strings.Contains(err.Error(), state.reason) || pods != nil {
						t.Fatalf("unread inventory=%v, %v; want %q", pods, err, state.reason)
					}
				}
			})
		}
	}
	for _, read := range []func(*k8s.ResourceCache, *unstructured.Unstructured) ([]*corev1.Pod, error){poolerPods, clusterInstancePods} {
		if pods, err := read(nil, object); err == nil || pods != nil {
			t.Fatalf("missing cache established absence: %v %v", pods, err)
		}
	}
}
