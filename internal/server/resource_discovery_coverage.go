package server

import (
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/capacityapi"
)

func resourceDiscoveryCoverage(discovery *k8s.ResourceDiscovery) *capacityapi.SourceCoverage {
	coverage := capacityapi.NewSourceCoverage(capacityapi.CoverageUnavailable, capacityapi.CoverageScopeCluster)
	coverage.ReasonCode = "not_initialized"
	coverage.ImpactFields = []string{"apiResources"}
	if discovery == nil {
		return &coverage
	}

	stats := discovery.Stats()
	if !stats.LastSuccessfulRefresh.IsZero() {
		observedAt := stats.LastSuccessfulRefresh
		coverage.ObservedAt = &observedAt
	}
	switch {
	case stats.Stale || (stats.LastSuccessfulRefresh.IsZero() && stats.LastError != ""):
		coverage.Status = capacityapi.CoverageError
		coverage.ReasonCode = "discovery_refresh_failed"
	case stats.Partial:
		coverage.Status = capacityapi.CoveragePartial
		coverage.ReasonCode = "partial_discovery"
	case stats.LastSuccessfulRefresh.IsZero():
		coverage.ReasonCode = "not_attempted"
	default:
		coverage.Status = capacityapi.CoverageAvailable
		coverage.ReasonCode = ""
		coverage.ImpactFields = []string{}
	}
	return &coverage
}
