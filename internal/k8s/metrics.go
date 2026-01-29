package k8s

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// PodMetrics represents metrics for a single pod
type PodMetrics struct {
	Metadata   MetricsMeta        `json:"metadata"`
	Timestamp  string             `json:"timestamp"`
	Window     string             `json:"window"`
	Containers []ContainerMetrics `json:"containers"`
}

// NodeMetrics represents metrics for a single node
type NodeMetrics struct {
	Metadata  MetricsMeta  `json:"metadata"`
	Timestamp string       `json:"timestamp"`
	Window    string       `json:"window"`
	Usage     ResourceUsage `json:"usage"`
}

// MetricsMeta contains metadata for metrics objects
type MetricsMeta struct {
	Name              string `json:"name"`
	Namespace         string `json:"namespace,omitempty"`
	CreationTimestamp string `json:"creationTimestamp"`
}

// ContainerMetrics represents metrics for a single container
type ContainerMetrics struct {
	Name  string        `json:"name"`
	Usage ResourceUsage `json:"usage"`
}

// ResourceUsage contains CPU and memory usage
type ResourceUsage struct {
	CPU    string `json:"cpu"`
	Memory string `json:"memory"`
}

var (
	podMetricsGVR = schema.GroupVersionResource{
		Group:    "metrics.k8s.io",
		Version:  "v1beta1",
		Resource: "pods",
	}
	nodeMetricsGVR = schema.GroupVersionResource{
		Group:    "metrics.k8s.io",
		Version:  "v1beta1",
		Resource: "nodes",
	}
)

// GetPodMetrics fetches metrics for a specific pod from the metrics.k8s.io API
func GetPodMetrics(ctx context.Context, namespace, name string) (*PodMetrics, error) {
	client := GetDynamicClient()
	if client == nil {
		return nil, fmt.Errorf("dynamic client not initialized")
	}

	result, err := client.Resource(podMetricsGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get pod metrics: %w", err)
	}

	// Convert unstructured to PodMetrics
	metrics := &PodMetrics{}

	// Extract metadata
	if meta, ok := result.Object["metadata"].(map[string]interface{}); ok {
		metrics.Metadata.Name, _ = meta["name"].(string)
		metrics.Metadata.Namespace, _ = meta["namespace"].(string)
		metrics.Metadata.CreationTimestamp, _ = meta["creationTimestamp"].(string)
	}

	// Extract timestamp and window
	metrics.Timestamp, _ = result.Object["timestamp"].(string)
	metrics.Window, _ = result.Object["window"].(string)

	// Extract containers
	if containers, ok := result.Object["containers"].([]interface{}); ok {
		for _, c := range containers {
			if container, ok := c.(map[string]interface{}); ok {
				cm := ContainerMetrics{}
				cm.Name, _ = container["name"].(string)
				if usage, ok := container["usage"].(map[string]interface{}); ok {
					cm.Usage.CPU, _ = usage["cpu"].(string)
					cm.Usage.Memory, _ = usage["memory"].(string)
				}
				metrics.Containers = append(metrics.Containers, cm)
			}
		}
	}

	return metrics, nil
}

// GetNodeMetrics fetches metrics for a specific node from the metrics.k8s.io API
func GetNodeMetrics(ctx context.Context, name string) (*NodeMetrics, error) {
	client := GetDynamicClient()
	if client == nil {
		return nil, fmt.Errorf("dynamic client not initialized")
	}

	result, err := client.Resource(nodeMetricsGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get node metrics: %w", err)
	}

	// Convert unstructured to NodeMetrics
	metrics := &NodeMetrics{}

	// Extract metadata
	if meta, ok := result.Object["metadata"].(map[string]interface{}); ok {
		metrics.Metadata.Name, _ = meta["name"].(string)
		metrics.Metadata.CreationTimestamp, _ = meta["creationTimestamp"].(string)
	}

	// Extract timestamp and window
	metrics.Timestamp, _ = result.Object["timestamp"].(string)
	metrics.Window, _ = result.Object["window"].(string)

	// Extract usage
	if usage, ok := result.Object["usage"].(map[string]interface{}); ok {
		metrics.Usage.CPU, _ = usage["cpu"].(string)
		metrics.Usage.Memory, _ = usage["memory"].(string)
	}

	return metrics, nil
}
