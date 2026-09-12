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
	globalClient.beylaJobSelector = beylaJobSelector
	return nil
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
