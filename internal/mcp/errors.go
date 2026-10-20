package mcp

import (
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/readreason"
)

// errNotConnected explains WHY the cluster is unavailable. MCP callers are
// usually AI agents: a bare "not connected" reads as a dead end, when a
// credential-loss demotion actually self-heals once the operator
// re-authenticates. The code says which of those it is; while connecting the
// message is the one REST gives for cluster_connecting.
func errNotConnected() error {
	status := k8s.GetConnectionStatus()
	if status.State == k8s.StateConnecting {
		return readErrorf(readreason.ClusterConnecting, "Cluster is still connecting, please retry shortly")
	}
	if status.State == k8s.StateDisconnected && status.Error != "" {
		switch status.ErrorType {
		case "auth-rejected":
			return readErrorf(readreason.ClusterDisconnected, "not connected to cluster: %s. The Kubernetes API returned HTTP 401, so check that the local credential source supplied a current credential and, where identities are mapped explicitly, that the principal has a cluster mapping (on EKS, an access entry or legacy aws-auth mapping). Radar reconnects automatically once authentication succeeds; POST /api/connection/retry forces an immediate check", status.Error)
		case "auth-plugin-stuck":
			return readErrorf(readreason.ClusterDisconnected, "not connected to cluster: %s. Check that the kubeconfig credential command and its identity-provider or network dependencies are responsive. Radar continues checking in the background; POST /api/connection/retry forces an immediate check", status.Error)
		case "auth":
			return readErrorf(readreason.ClusterDisconnected, "not connected to cluster: %s. Radar re-checks in the background and reconnects automatically once credentials are refreshed; POST /api/connection/retry forces an immediate check", status.Error)
		}
		return readErrorf(readreason.ClusterDisconnected, "not connected to cluster: %s. %s", status.Error, restoreConnection)
	}
	return readErrorf(readreason.ClusterDisconnected, "not connected to cluster. %s", restoreConnection)
}

// restoreConnection is the remedy when Radar does not recover on its own:
// outside credential failures, a retry is what reconnects.
const restoreConnection = "Whoever runs Radar must restore its connection to the cluster (network, API endpoint or credentials); POST /api/connection/retry forces an immediate check"

// requireConnected gates tools that read stores the cache feeds, such as the
// change timeline: before the cluster has connected they are empty, and an
// empty answer would read as "nothing happened".
func requireConnected() error {
	if k8s.IsConnected() {
		return nil
	}
	return errNotConnected()
}
