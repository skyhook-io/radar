package server

import (
	"context"
	"encoding/json"
	"log"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/skyhook-io/radar/internal/capacity"
	"github.com/skyhook-io/radar/internal/k8s"
)

// Shared metrics-server plumbing — consumed by both /api/dashboard and
// /api/vitals. Lives in neither handler file on purpose: the probe is a
// cluster-metrics concern, not a feature of either endpoint.

// metricsServerTimeout bounds the metrics-server query so a slow or unreachable
// metrics endpoint can't consume the dashboard's whole request budget (the
// handler runs this in a goroutine and blocks on it in wg.Wait()).
const metricsServerTimeout = 8 * time.Second

// fetchNodeUsage probes metrics-server for live node usage (impersonated —
// a caller without metrics.k8s.io access gets ok=false, not someone else's
// numbers). Extracted so /api/vitals can memoize JUST this probe while
// computing capacity/requests fresh from the informer cache on every call.
func (s *Server) fetchNodeUsage(ctx context.Context) (cpuMillis, memBytes int64, ok bool) {
	client := k8s.ClientFromContext(ctx)
	if client == nil {
		return 0, 0, false
	}
	mctx, cancel := context.WithTimeout(ctx, metricsServerTimeout)
	defer cancel()
	metricsPath, ok := k8s.MetricsAPIPath("nodes")
	if !ok {
		return 0, 0, false
	}
	data, err := client.CoreV1().RESTClient().Get().
		AbsPath(metricsPath).
		DoRaw(mctx)
	if err != nil {
		log.Printf("[dashboard] node metrics unavailable (showing requests/capacity only): %v", err)
		return 0, 0, false
	}
	var nodeMetricsList struct {
		Items []struct {
			Usage struct {
				CPU    string `json:"cpu"`
				Memory string `json:"memory"`
			} `json:"usage"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &nodeMetricsList); err != nil {
		log.Printf("[dashboard] failed to parse node metrics: %v", err)
		return 0, 0, false
	}
	if len(nodeMetricsList.Items) == 0 {
		return 0, 0, false
	}
	for _, item := range nodeMetricsList.Items {
		cpuMillis += parseCPUToMillis(item.Usage.CPU)
		memBytes += parseMemoryToBytes(item.Usage.Memory)
	}
	return cpuMillis, memBytes, true
}

// parseCPUToMillis delegates to k8s.ParseCPUToMillis.
func parseCPUToMillis(s string) int64 { return k8s.ParseCPUToMillis(s) }

// parseMemoryToBytes delegates to k8s.ParseMemoryToBytes.
func parseMemoryToBytes(s string) int64 { return k8s.ParseMemoryToBytes(s) }

// capacityRequests carries the informer-derived halves of the capacity
// picture: node allocatable capacity plus scheduled-pod requests (completed pods
// excluded). Usage is the metrics-server probe's job (fetchNodeUsage).
type capacityRequests struct {
	cpuCapMillis int64
	memCapBytes  int64
	cpuReqMillis int64
	memReqBytes  int64
}

func computeCapacityRequests(nodes []*corev1.Node, pods []*corev1.Pod) capacityRequests {
	accounting := capacity.AccountResources(nodes, pods)
	return capacityRequests{
		cpuCapMillis: accounting.Allocatable.Cpu().MilliValue(),
		memCapBytes:  accounting.Allocatable.Memory().Value(),
		cpuReqMillis: accounting.ScheduledRequests.Cpu().MilliValue(),
		memReqBytes:  accounting.ScheduledRequests.Memory().Value(),
	}
}
