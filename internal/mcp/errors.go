package mcp

import (
	"errors"
	"fmt"
	"strings"

	"github.com/skyhook-io/radar/internal/k8s"
)

// errNotConnected explains WHY the cluster is unavailable. MCP callers are
// usually AI agents: a bare "not connected" reads as a dead end, when a
// credential-loss demotion actually self-heals once the operator
// re-authenticates.
func errNotConnected() error {
	status := k8s.GetConnectionStatus()
	if status.State == k8s.StateDisconnected && status.Error != "" {
		if strings.HasPrefix(status.ErrorType, "auth") {
			return fmt.Errorf("not connected to cluster: %s. Radar re-checks in the background and reconnects automatically once credentials are refreshed; POST /api/connection/retry forces an immediate check", status.Error)
		}
		return fmt.Errorf("not connected to cluster: %s", status.Error)
	}
	return errors.New("not connected to cluster")
}
