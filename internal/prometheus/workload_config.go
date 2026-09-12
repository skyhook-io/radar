package prometheus

import (
	"fmt"
	"maps"

	"github.com/skyhook-io/radar/pkg/prom"
)

// SetWorkloadMetricsScope binds an operator assertion to the current client.
// URL/header changes and context reinitialization discard it deliberately.
func SetWorkloadMetricsScope(scope prom.WorkloadMetricsScope, beylaJobSelector string) error {
	if _, err := scope.Matchers(); err != nil {
		return err
	}
	clientMu.RLock()
	defer clientMu.RUnlock()
	if globalClient == nil {
		return fmt.Errorf("Prometheus client not initialized")
	}
	globalClient.mu.Lock()
	defer globalClient.mu.Unlock()
	scope.ClusterLabels = maps.Clone(scope.ClusterLabels)
	globalClient.workloadScope = &scope
	globalClient.workloadScopeEverSet = true
	globalClient.beylaJobSelector = beylaJobSelector
	return nil
}

func (c *Client) workloadScopeNotice() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.workloadScopeEverSet && c.workloadScope == nil {
		return "The operator's metrics scope assertion was discarded after a connection change. Automatic matching is active. To override it, first verify the backend scope for the currently connected cluster, then restart Radar with the appropriate scope flags."
	}
	return ""
}

func (c *Client) workloadMetricsConfig() (prom.WorkloadMetricsScope, string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.retired || c.workloadScope == nil {
		return prom.WorkloadMetricsScope{}, "", false
	}
	scope := *c.workloadScope
	scope.ClusterLabels = maps.Clone(scope.ClusterLabels)
	return scope, c.beylaJobSelector, true
}
