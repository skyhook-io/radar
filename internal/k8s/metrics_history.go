package k8s

import (
	"github.com/skyhook-io/radar/internal/errorlog"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// Re-export types from pkg/k8score for backward compatibility.
type MetricsDataPoint = k8score.MetricsDataPoint
type ContainerMetricsHistory = k8score.ContainerMetricsHistory
type PodMetricsHistory = k8score.PodMetricsHistory
type NodeMetricsHistory = k8score.NodeMetricsHistory
type TopPodMetrics = k8score.TopPodMetrics
type ContainerResourceMetrics = k8score.ContainerResourceMetrics
type TopNodeMetrics = k8score.TopNodeMetrics
type MetricsCollectionHealth = k8score.MetricsCollectionHealth
type MetricsSourceHealth = k8score.MetricsSourceHealth
type MetricsHistoryStore = k8score.MetricsHistoryStore

const (
	MetricsHistorySize  = k8score.MetricsHistorySize
	MetricsPollInterval = k8score.MetricsPollInterval
)

// InitMetricsHistory initializes the metrics history store and starts polling.
func InitMetricsHistory() {
	Current().initMetricsHistory()
}

func (c *Connection) initMetricsHistory() {
	if c == nil {
		return
	}
	c.metricsMu.Lock()
	defer c.metricsMu.Unlock()
	if c.metricsClosed || c.metricsHistory.Load() != nil {
		return
	}
	store := k8score.NewMetricsHistoryStoreWithPollResolver(c.Dynamic(), c.metricsPollResolver())
	store.Namespaces = RBACNamespaces
	store.OnError = errorlog.Record
	store.Start()
	c.metricsHistory.Store(store)
}

func (c *Connection) MetricsHistory() *MetricsHistoryStore {
	if c == nil {
		return nil
	}
	return c.metricsHistory.Load()
}

// GetMetricsHistory returns the metrics history store.
func GetMetricsHistory() *MetricsHistoryStore {
	return Current().MetricsHistory()
}

// StopMetricsHistory stops the metrics polling.
func StopMetricsHistory() {
	if store := GetMetricsHistory(); store != nil {
		store.Stop()
	}
}

// ResetMetricsHistory closes the installed connection's metrics history.
func ResetMetricsHistory() {
	Current().closeMetricsHistory()
}

func (c *Connection) closeMetricsHistory() {
	if c == nil {
		return
	}
	c.metricsMu.Lock()
	defer c.metricsMu.Unlock()
	if store := c.metricsHistory.Load(); store != nil {
		store.Stop()
		c.metricsHistory.Store(nil)
	}
	c.metricsClosed = true
}
