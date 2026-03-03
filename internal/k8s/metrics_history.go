package k8s

import (
	"sync"

	"github.com/skyhook-io/radar/internal/errorlog"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// Re-export types from pkg/k8score for backward compatibility.
type MetricsDataPoint = k8score.MetricsDataPoint
type ContainerMetricsHistory = k8score.ContainerMetricsHistory
type PodMetricsHistory = k8score.PodMetricsHistory
type NodeMetricsHistory = k8score.NodeMetricsHistory
type TopPodMetrics = k8score.TopPodMetrics
type TopNodeMetrics = k8score.TopNodeMetrics
type MetricsCollectionHealth = k8score.MetricsCollectionHealth
type MetricsSourceHealth = k8score.MetricsSourceHealth
type MetricsHistoryStore = k8score.MetricsHistoryStore

const (
	MetricsHistorySize  = k8score.MetricsHistorySize
	MetricsPollInterval = k8score.MetricsPollInterval
)

var (
	metricsHistoryStore *MetricsHistoryStore
	metricsHistoryOnce  = new(sync.Once)
	metricsHistoryMu    sync.Mutex
)

// InitMetricsHistory initializes the metrics history store and starts polling.
func InitMetricsHistory() {
	metricsHistoryMu.Lock()
	defer metricsHistoryMu.Unlock()
	metricsHistoryOnce.Do(func() {
		metricsHistoryStore = k8score.NewMetricsHistoryStore(GetDynamicClient())
		metricsHistoryStore.OnError = func(subsystem, level, format string, args ...any) {
			errorlog.Record(subsystem, level, format, args...)
		}
		metricsHistoryStore.Start()
	})
}

// GetMetricsHistory returns the metrics history store.
func GetMetricsHistory() *MetricsHistoryStore {
	return metricsHistoryStore
}

// StopMetricsHistory stops the metrics polling.
func StopMetricsHistory() {
	if metricsHistoryStore != nil {
		metricsHistoryStore.Stop()
	}
}

// ResetMetricsHistory stops polling and clears the store so it can be
// reinitialized for a new cluster after context switch.
func ResetMetricsHistory() {
	metricsHistoryMu.Lock()
	defer metricsHistoryMu.Unlock()
	StopMetricsHistory()
	metricsHistoryStore = nil
	metricsHistoryOnce = new(sync.Once)
}
