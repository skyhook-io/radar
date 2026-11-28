package prometheus

import (
	"maps"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

func (c *Client) SetWorkloadMetricsScope(scope prom.WorkloadMetricsScope, beylaJobSelector string) error {
	if _, err := scope.Matchers(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	scope.ClusterLabels = maps.Clone(scope.ClusterLabels)
	c.gkeRevision++
	c.gke = nil
	c.gkeChecked = time.Time{}
	if relay := c.relay; relay != nil {
		// The relay's scope stays bound; this one applies when it ends.
		relay.setAside(func(s *prometheusSettings) {
			s.scope, s.scopeEverSet, s.beylaJobSelector = &scope, true, beylaJobSelector
		})
		return nil
	}
	c.workloadScope = &scope
	c.workloadScopeEverSet = true
	c.beylaJobSelector = beylaJobSelector
	return nil
}

func (c *Client) NoteWorkloadScopeCleared() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.workloadScopeEverSet = true
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
