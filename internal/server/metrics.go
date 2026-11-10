package server

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/perfstats"
)

// metricsTimelinePingTimeout bounds the database round trip a scrape makes, so a
// database that stops answering cannot hold the scrape open.
const metricsTimelinePingTimeout = 2 * time.Second

// metricsTimelineCheckTTL is how long a scrape reuses the last database check.
// The endpoint can be reachable without signing in, so without it any client
// could make Radar ping PostgreSQL as often as it likes. Scrape intervals are
// 15s or more, so a fresh check still lands on nearly every scrape.
const metricsTimelineCheckTTL = 5 * time.Second

// timelineAvailability caches the result of the database check for
// metricsTimelineCheckTTL. Scrapes that arrive while a check is running wait
// for it instead of starting their own.
type timelineAvailability struct {
	mu        sync.Mutex
	checkedAt time.Time
	available bool
}

func (a *timelineAvailability) value() float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.checkedAt.IsZero() || time.Since(a.checkedAt) >= metricsTimelineCheckTTL {
		ctx, cancel := context.WithTimeout(context.Background(), metricsTimelinePingTimeout)
		a.available = timeline.Unavailable(ctx) == nil
		cancel()
		a.checkedAt = time.Now()
	}
	if a.available {
		return 1
	}
	return 0
}

func (s *Server) newMetricsHandler() http.Handler {
	registry := prometheus.NewRegistry()
	timelineCheck := &timelineAvailability{}

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
		// The timeline can be down while every other view works, so nothing
		// else on the page or in the health check shows it. This is the signal
		// to alert on. Only the 0/1 goes out: the reason names the database
		// host, and this endpoint can be reachable without signing in.
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "radar",
			Subsystem: "timeline",
			Name:      "available",
			Help:      "1 when the timeline can record and serve history, 0 when its storage is unreachable. The reason is in Radar's diagnostics.",
		}, timelineCheck.value),
		timelineDropsCollector{desc: prometheus.NewDesc(
			"radar_timeline_dropped_events_total",
			"Changes the timeline did not record, by reason. store_unavailable and store_failed are history lost to a storage outage; the other reasons are routine filtering.",
			[]string{"reason"}, nil,
		)},
	)

	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}

// timelineDropsCollector exports the timeline's drop counters with the reason as
// a label. The counters live in the timeline package and are reset on a context
// switch, which Prometheus reads as a counter reset.
type timelineDropsCollector struct{ desc *prometheus.Desc }

func (c timelineDropsCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c timelineDropsCollector) Collect(ch chan<- prometheus.Metric) {
	for reason, n := range timeline.GetMetrics().GetSnapshot().Counters.Dropped {
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.CounterValue, float64(n), reason)
	}
}
