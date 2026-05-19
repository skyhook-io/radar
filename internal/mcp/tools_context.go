package mcp

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/k8s"
)

type listContextsInput struct{}

type switchContextInput struct {
	Name string `json:"name" jsonschema:"description=kubeconfig context name to switch to"`
}

func handleListContexts(ctx context.Context, req *mcp.CallToolRequest, _ listContextsInput) (*mcp.CallToolResult, any, error) {
	contexts, err := k8s.GetAvailableContexts()
	if err != nil {
		return nil, nil, err
	}
	return toJSONResult(contexts)
}

func handleSwitchContext(ctx context.Context, req *mcp.CallToolRequest, input switchContextInput) (*mcp.CallToolResult, any, error) {
	if input.Name == "" {
		return nil, nil, fmt.Errorf("context name is required")
	}
	if k8s.IsInCluster() {
		return nil, nil, fmt.Errorf("cannot switch context when running in-cluster")
	}
	if err := k8s.PerformContextSwitch(input.Name); err != nil {
		k8s.SetConnectionStatus(k8s.ConnectionStatus{
			State:   k8s.StateDisconnected,
			Context: input.Name,
			Error:   err.Error(),
		})
		return nil, nil, err
	}
	k8s.SetConnectionStatus(k8s.ConnectionStatus{
		State:       k8s.StateConnected,
		Context:     k8s.GetContextName(),
		ClusterName: k8s.GetClusterName(),
	})
	return toJSONResult(map[string]string{
		"status":  "ok",
		"context": k8s.GetContextName(),
		"cluster": k8s.GetClusterName(),
	})
}
