package k8s

import (
	"context"
	"fmt"

	"github.com/skyhook-io/radar/pkg/k8score"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Re-export types from pkg/k8score for backward compatibility with existing callers.
type PodMetrics = k8score.PodMetrics
type NodeMetrics = k8score.NodeMetrics
type MetricsMeta = k8score.MetricsMeta
type ContainerMetrics = k8score.ContainerMetrics
type ResourceUsage = k8score.ResourceUsage

func ResolveMetricsGVR(resource string) (schema.GroupVersionResource, bool) {
	discovery := GetResourceDiscovery()
	if discovery == nil {
		return schema.GroupVersionResource{}, false
	}
	_ = discovery.RefreshIfStale()
	return discovery.LookupRefreshingOnMiss(func(d *ResourceDiscovery) (schema.GroupVersionResource, bool) {
		return d.GetGVRWithGroup(resource, k8score.MetricsAPIGroup)
	})
}

// Each source consumes the result of its previous refresh before requesting the
// next one. Otherwise a persistent miss starts a refresh on every poll and the
// poller never reports the absence confirmed by the preceding refresh.
func (c *Connection) metricsPollResolver() func(string) k8score.MetricsPollDiscovery {
	previous := make(map[string]<-chan struct{})
	return func(resource string) k8score.MetricsPollDiscovery {
		d := c.Discovery()
		if d == nil || d.ResourceDiscovery == nil {
			return k8score.MetricsPollDiscovery{RefreshPending: true}
		}
		confirmed := false
		select {
		case <-previous[resource]:
			confirmed = true
		default:
		}
		lookup := func(d *ResourceDiscovery) (schema.GroupVersionResource, bool) {
			return d.GetGVRWithGroup(resource, k8score.MetricsAPIGroup)
		}
		gvr, found := lookup(d)
		if found {
			refresh := d.refreshIfStaleInBackground()
			previous[resource] = refresh
			return k8score.MetricsPollDiscovery{GVR: gvr, RefreshPending: refresh != nil}
		}
		pending := !confirmed || d.LastRefreshFailed() || d.GroupHadPartialDiscovery(k8score.MetricsAPIGroup)
		// A credential plugin can stall discovery indefinitely, so the miss
		// refresh must not retain the poller or its connection.
		previous[resource] = d.refreshInBackground(func() {
			d.LookupRefreshingOnMiss(lookup)
		})
		return k8score.MetricsPollDiscovery{RefreshPending: pending}
	}
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
