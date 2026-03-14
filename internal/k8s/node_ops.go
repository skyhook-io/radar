package k8s

import (
	"context"
	"fmt"

	"github.com/skyhook-io/radar/pkg/k8score"
)

// DrainOptions is an alias for the reusable DrainOptions type.
type DrainOptions = k8score.DrainOptions

// DrainResult is an alias for the reusable DrainResult type.
type DrainResult = k8score.DrainResult

// CordonNode marks a node as unschedulable.
func CordonNode(ctx context.Context, nodeName string) error {
	client := GetClient()
	if client == nil {
		return fmt.Errorf("not connected to cluster")
	}
	return k8score.CordonNode(ctx, client, nodeName)
}

// UncordonNode marks a node as schedulable.
func UncordonNode(ctx context.Context, nodeName string) error {
	client := GetClient()
	if client == nil {
		return fmt.Errorf("not connected to cluster")
	}
	return k8score.UncordonNode(ctx, client, nodeName)
}

// DrainNode cordons the node and evicts all eligible pods.
func DrainNode(ctx context.Context, nodeName string, opts DrainOptions) (*DrainResult, error) {
	client := GetClient()
	if client == nil {
		return nil, fmt.Errorf("not connected to cluster")
	}
	return k8score.DrainNode(ctx, client, nodeName, opts)
}
