package k8s

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Re-export types from pkg/k8score for backward compatibility with existing callers.
type PodMetrics = k8score.PodMetrics
type NodeMetrics = k8score.NodeMetrics
type MetricsMeta = k8score.MetricsMeta
type ContainerMetrics = k8score.ContainerMetrics
type ResourceUsage = k8score.ResourceUsage

const metricsDiscoveryMissRefreshInterval = 30 * time.Second

var metricsDiscoveryMissRefresh = struct {
	sync.Mutex
	discovery *ResourceDiscovery
	at        time.Time
}{}

func ResolveMetricsGVR(resource string) (schema.GroupVersionResource, bool) {
	discovery := GetResourceDiscovery()
	if discovery == nil {
		return schema.GroupVersionResource{}, false
	}
	_ = discovery.RefreshIfStale()
	if gvr, ok := discovery.GetGVRWithGroup(resource, k8score.MetricsAPIGroup); ok {
		return gvr, true
	}

	metricsDiscoveryMissRefresh.Lock()
	defer metricsDiscoveryMissRefresh.Unlock()
	if gvr, ok := discovery.GetGVRWithGroup(resource, k8score.MetricsAPIGroup); ok {
		return gvr, true
	}
	if metricsDiscoveryMissRefresh.discovery != discovery {
		metricsDiscoveryMissRefresh.discovery = discovery
		metricsDiscoveryMissRefresh.at = time.Time{}
	}
	if time.Since(metricsDiscoveryMissRefresh.at) < metricsDiscoveryMissRefreshInterval {
		return schema.GroupVersionResource{}, false
	}
	metricsDiscoveryMissRefresh.at = time.Now()
	if err := discovery.Refresh(); err != nil {
		return schema.GroupVersionResource{}, false
	}
	return discovery.GetGVRWithGroup(resource, k8score.MetricsAPIGroup)
}

func MetricsAPIPath(resource string) (string, bool) {
	gvr, ok := ResolveMetricsGVR(resource)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("/apis/%s/%s/%s", gvr.Group, gvr.Version, gvr.Resource), true
}

// GetPodMetrics fetches metrics for a specific pod from the metrics.k8s.io API.
func GetPodMetrics(ctx context.Context, namespace, name string) (*PodMetrics, error) {
	client := GetDynamicClient()
	if client == nil {
		return nil, fmt.Errorf("dynamic client not initialized")
	}
	gvr, ok := ResolveMetricsGVR("pods")
	if !ok {
		return nil, k8score.ErrMetricsAPINotDiscovered
	}
	return k8score.GetPodMetricsWithGVR(ctx, client, gvr, namespace, name)
}

// GetNodeMetrics fetches metrics for a specific node from the metrics.k8s.io API.
func GetNodeMetrics(ctx context.Context, name string) (*NodeMetrics, error) {
	client := GetDynamicClient()
	if client == nil {
		return nil, fmt.Errorf("dynamic client not initialized")
	}
	gvr, ok := ResolveMetricsGVR("nodes")
	if !ok {
		return nil, k8score.ErrMetricsAPINotDiscovered
	}
	return k8score.GetNodeMetricsWithGVR(ctx, client, gvr, name)
}
