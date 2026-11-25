package prometheus

import (
	"fmt"
	"maps"
	"time"

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
	globalClient.gkeRevision++
	globalClient.gke = nil
	globalClient.gkeChecked = time.Time{}
	if relay := globalClient.relay; relay != nil {
		// The relay's scope stays bound; this one applies when it ends.
		relay.setAside(func(s *prometheusSettings) {
			s.scope, s.scopeEverSet, s.beylaJobSelector = &scope, true, beylaJobSelector
		})
		return nil
	}
	globalClient.workloadScope = &scope
	globalClient.workloadScopeEverSet = true
	globalClient.beylaJobSelector = beylaJobSelector
	return nil
}

func (c *Client) workloadScopeNotice() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.workloadScopeEverSet && c.workloadScope == nil {
		return "The previous cluster-scope override was cleared because the cluster or metrics connection changed. Radar is checking metric identity automatically."
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
