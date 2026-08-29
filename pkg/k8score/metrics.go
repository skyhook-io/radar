package k8score

import (
	"context"
	"errors"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// MetricsAPIGroup is the Kubernetes resource metrics API group.
const MetricsAPIGroup = "metrics.k8s.io"

var (
	// PodMetricsGVR is the v1beta1 compatibility default used by GetPodMetrics.
	// Discovery-aware callers should use GetPodMetricsWithGVR.
	PodMetricsGVR = schema.GroupVersionResource{Group: MetricsAPIGroup, Version: "v1beta1", Resource: "pods"}
	// NodeMetricsGVR is the v1beta1 compatibility default used by GetNodeMetrics.
	// Discovery-aware callers should use GetNodeMetricsWithGVR.
	NodeMetricsGVR = schema.GroupVersionResource{Group: MetricsAPIGroup, Version: "v1beta1", Resource: "nodes"}
	// ErrMetricsAPINotDiscovered indicates that API discovery did not return a
	// usable metrics API version.
	ErrMetricsAPINotDiscovered = errors.New("metrics API is not discovered")
)

// MetricsGVRResolver resolves the currently served GVR for a metrics resource.
type MetricsGVRResolver func(resource string) (schema.GroupVersionResource, bool)

// PodMetrics represents metrics for a single pod.
type PodMetrics struct {
	Metadata   MetricsMeta        `json:"metadata"`
	Timestamp  string             `json:"timestamp"`
	Window     string             `json:"window"`
	Containers []ContainerMetrics `json:"containers"`
}

// NodeMetrics represents metrics for a single node.
type NodeMetrics struct {
	Metadata  MetricsMeta   `json:"metadata"`
	Timestamp string        `json:"timestamp"`
	Window    string        `json:"window"`
	Usage     ResourceUsage `json:"usage"`
}

// MetricsMeta contains metadata for metrics objects.
type MetricsMeta struct {
	Name              string `json:"name"`
	Namespace         string `json:"namespace,omitempty"`
	CreationTimestamp string `json:"creationTimestamp"`
}

// ContainerMetrics represents metrics for a single container.
type ContainerMetrics struct {
	Name  string        `json:"name"`
	Usage ResourceUsage `json:"usage"`
}

// ResourceUsage contains CPU and memory usage.
type ResourceUsage struct {
	CPU    string `json:"cpu"`
	Memory string `json:"memory"`
}

// GetPodMetrics fetches metrics for a specific pod from the metrics.k8s.io API.
func GetPodMetrics(ctx context.Context, client dynamic.Interface, namespace, name string) (*PodMetrics, error) {
	return GetPodMetricsWithGVR(ctx, client, PodMetricsGVR, namespace, name)
}

// GetPodMetricsWithGVR fetches metrics using a discovered metrics API version.
func GetPodMetricsWithGVR(ctx context.Context, client dynamic.Interface, gvr schema.GroupVersionResource, namespace, name string) (*PodMetrics, error) {
	result, err := client.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get pod metrics: %w", err)
	}

	metrics := &PodMetrics{}

	if meta, ok := result.Object["metadata"].(map[string]any); ok {
		metrics.Metadata.Name, _ = meta["name"].(string)
		metrics.Metadata.Namespace, _ = meta["namespace"].(string)
		metrics.Metadata.CreationTimestamp, _ = meta["creationTimestamp"].(string)
	}

	metrics.Timestamp, _ = result.Object["timestamp"].(string)
	metrics.Window, _ = result.Object["window"].(string)

	if containers, ok := result.Object["containers"].([]any); ok {
		for _, c := range containers {
			if container, ok := c.(map[string]any); ok {
				cm := ContainerMetrics{}
				cm.Name, _ = container["name"].(string)
				if usage, ok := container["usage"].(map[string]any); ok {
					cm.Usage.CPU, _ = usage["cpu"].(string)
					cm.Usage.Memory, _ = usage["memory"].(string)
				}
				metrics.Containers = append(metrics.Containers, cm)
			}
		}
	}

	return metrics, nil
}

// GetNodeMetrics fetches metrics for a specific node from the metrics.k8s.io API.
func GetNodeMetrics(ctx context.Context, client dynamic.Interface, name string) (*NodeMetrics, error) {
	return GetNodeMetricsWithGVR(ctx, client, NodeMetricsGVR, name)
}

// GetNodeMetricsWithGVR fetches metrics using a discovered metrics API version.
func GetNodeMetricsWithGVR(ctx context.Context, client dynamic.Interface, gvr schema.GroupVersionResource, name string) (*NodeMetrics, error) {
	result, err := client.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get node metrics: %w", err)
	}

	metrics := &NodeMetrics{}

	if meta, ok := result.Object["metadata"].(map[string]any); ok {
		metrics.Metadata.Name, _ = meta["name"].(string)
		metrics.Metadata.CreationTimestamp, _ = meta["creationTimestamp"].(string)
	}

	metrics.Timestamp, _ = result.Object["timestamp"].(string)
	metrics.Window, _ = result.Object["window"].(string)

	if usage, ok := result.Object["usage"].(map[string]any); ok {
		metrics.Usage.CPU, _ = usage["cpu"].(string)
		metrics.Usage.Memory, _ = usage["memory"].(string)
	}

	return metrics, nil
}
