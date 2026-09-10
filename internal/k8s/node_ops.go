package k8s

import (
	"context"
	"fmt"

	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/pkg/k8score"
)

// DrainOptions is an alias for the reusable DrainOptions type.
type DrainOptions = k8score.DrainOptions

// DrainResult is an alias for the reusable DrainResult type.
type DrainResult = k8score.DrainResult

// DrainPlan is an alias for the reusable DrainPlan type.
type DrainPlan = k8score.DrainPlan

// CordonNode marks a node as unschedulable.
func CordonNode(ctx context.Context, nodeName string) error {
	client := GetClient()
	if client == nil {
		return fmt.Errorf("not connected to cluster")
	}
	return k8score.CordonNode(ctx, client, nodeName)
}

// CordonNodeWithClient is CordonNode that uses the caller-supplied client
// (so user impersonation — enforcing K8s RBAC on the caller — can be applied).
func CordonNodeWithClient(ctx context.Context, nodeName string, client kubernetes.Interface) error {
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

// UncordonNodeWithClient is UncordonNode with a caller-supplied client.
func UncordonNodeWithClient(ctx context.Context, nodeName string, client kubernetes.Interface) error {
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

// DrainNodeWithClient is DrainNode with a caller-supplied client.
func DrainNodeWithClient(ctx context.Context, nodeName string, opts DrainOptions, client kubernetes.Interface) (*DrainResult, error) {
	if client == nil {
		return nil, fmt.Errorf("not connected to cluster")
	}
	return k8score.DrainNode(ctx, client, nodeName, opts)
}

// PlanNodeDrainWithClient computes a read-only drain plan with a caller-supplied client
// (so the plan is evaluated under the caller's RBAC identity). It never mutates the cluster.
func PlanNodeDrainWithClient(ctx context.Context, nodeName string, opts DrainOptions, client kubernetes.Interface) (*DrainPlan, error) {
	if client == nil {
		return nil, fmt.Errorf("not connected to cluster")
	}
	return k8score.PlanNodeDrain(ctx, client, nodeName, opts)
}
