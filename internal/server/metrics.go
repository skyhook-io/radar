package server

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/skyhook-io/radar/pkg/perfstats"
)

func (s *Server) newMetricsHandler() http.Handler {
	registry := prometheus.NewRegistry()

	registry.MustRegister(
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "radar",
			Subsystem: "sse",
			Name:      "connected_clients",
			Help:      "Current number of clients connected to Radar's SSE stream.",
		}, func() float64 {
			return float64(s.broadcaster.ClientCount())
		}),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Namespace: "radar",
			Subsystem: "sse",
			Name:      "topology_broadcasts_total",
			Help:      "Total number of topology broadcast cycles sent over Radar's SSE stream.",
		}, func() float64 {
			return float64(perfstats.GetSSEStats().TotalBroadcasts)
		}),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Namespace: "radar",
			Subsystem: "sse",
			Name:      "dropped_events_total",
			Help:      "Total number of SSE events dropped because a client channel was full.",
		}, func() float64 {
			return float64(perfstats.GetSSEStats().TotalDrops)
		}),
	)

	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}
