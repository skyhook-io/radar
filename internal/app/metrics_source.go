package app

import (
	"log"
	"slices"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/connectionruntime"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/prom"
)

// ApplyHubMetricsSource applies the metrics source Radar Hub selected for this
// cluster. OpenObserve routes the metrics client through Hub's relay, which
// enforces the cluster matchers; the first matcher is kept for display.
// Prometheus restores the configured URL or discovery exactly as it was before.
func ApplyHubMetricsSource(runtime *connectionruntime.Runtime, d cloud.MetricsSourceDecision) {
	if d.Response.Source != cloud.MetricsSourceOpenObserve {
		runtime.SetRelay(nil)
		return
	}
	source := prom.MetricsSource{
		Kind:            cloud.MetricsSourceOpenObserve,
		Via:             "hub",
		MatchedBy:       d.Response.MatchedBy,
		Dialect:         d.Response.Dialect,
		Warning:         d.Response.Warning,
		VersionWarning:  d.Response.VersionWarning,
		MissingFamilies: slices.Clone(d.Response.MissingFamilies),
	}
	if len(d.Response.Matchers) > 0 {
		m := d.Response.Matchers[0]
		source.ClusterLabel = &prom.MetricsSourceLabel{Name: m.Name, Value: m.Value}
	}
	err := runtime.SetRelay(&prometheuspkg.RelayConfig{
		Key:          d.Key,
		Endpoint:     d.Endpoint,
		Address:      cloud.MetricsRelayAddress,
		Source:       source,
		NewTransport: d.NewTransport,
		Health:       d.Health,
	})
	if err != nil {
		log.Printf("[cloud] OpenObserve metrics source not applied: %v", err)
	}
}
