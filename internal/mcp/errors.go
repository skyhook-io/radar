package mcp

import (
	"errors"
	"fmt"

	"github.com/skyhook-io/radar/internal/k8s"
)

// errNotConnected explains WHY the cluster is unavailable. MCP callers are
// usually AI agents: a bare "not connected" reads as a dead end, when a
// credential-loss demotion actually self-heals once the operator
// re-authenticates.
func errNotConnected() error {
	status := k8s.GetConnectionStatus()
	if status.State == k8s.StateDisconnected && status.Error != "" {
		switch status.ErrorType {
		case "auth-rejected":
			return fmt.Errorf("not connected to cluster: %s. The Kubernetes API returned HTTP 401, so check that the local credential source supplied a current credential and, where identities are mapped explicitly, that the principal has a cluster mapping (on EKS, an access entry or legacy aws-auth mapping). Radar reconnects automatically once authentication succeeds; POST /api/connection/retry forces an immediate check", status.Error)
		case "auth-plugin-stuck":
			return fmt.Errorf("not connected to cluster: %s. Check that the kubeconfig credential command and its identity-provider or network dependencies are responsive. Radar continues checking in the background; POST /api/connection/retry forces an immediate check", status.Error)
		case "auth":
			return fmt.Errorf("not connected to cluster: %s. Radar re-checks in the background and reconnects automatically once credentials are refreshed; POST /api/connection/retry forces an immediate check", status.Error)
		}
		return fmt.Errorf("not connected to cluster: %s", status.Error)
	}
	return errors.New("not connected to cluster")
}
