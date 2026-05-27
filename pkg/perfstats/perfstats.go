// Package perfstats provides always-on, lightweight performance counters
// and sample windows for the diagnostics endpoint. All operations are cheap
// (atomic counter increment, or a fixed-size ring-buffer append under a
// short-lived mutex) and safe to call from hot paths.
//
// The data shape is shared with the frontend diagnostics overlay so users
// can include perf state in bug reports without enabling any flag.
package perfstats

import (
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// ringSize is the per-metric sample window. Sized so percentiles are
// meaningful but memory stays trivial (100 × int64 = 800 bytes per metric).
const ringSize = 100

// Snapshot is the rendered view of all counters + sample windows.
type Snapshot struct {
	Topology TopologyStats `json:"topology"`
	SSE      SSEStats      `json:"sse"`
}

// TopologyStats covers the topology build hot path.
type TopologyStats struct {
	TotalBuilds    int64        `json:"totalBuilds"`
	DurationUs     SampleWindow `json:"durationUs"`
	NodeCount      SampleWindow `json:"nodeCount"`
	EdgeCount      SampleWindow `json:"edgeCount"`
	PayloadBytes   SampleWindow `json:"payloadBytes"`
	EstimatedNodes SampleWindow `json:"estimatedNodes"`
}

// SSEStats covers the SSE broadcaster.
type SSEStats struct {
	TotalBroadcasts int64 `json:"totalBroadcasts"`
	TotalDrops      int64 `json:"totalDrops"`
}

// SampleWindow is the rendered view of one ring buffer.
type SampleWindow struct {
	Count int   `json:"count"` // samples in the window (capped at ringSize)
	Last  int64 `json:"last"`
	Min   int64 `json:"min"`
	P50   int64 `json:"p50"`
	P95   int64 `json:"p95"`
	P99   int64 `json:"p99"`
	Max   int64 `json:"max"`
}

type ringBuffer struct {
	mu      sync.Mutex
	samples [ringSize]int64
	count   int
	next    int
	last    int64
}

func (r *ringBuffer) add(v int64) {
	r.mu.Lock()
	r.samples[r.next] = v
	r.next = (r.next + 1) % ringSize
	if r.count < ringSize {
		r.count++
	}
	r.last = v
	r.mu.Unlock()
}

func (r *ringBuffer) snapshot() SampleWindow {
	r.mu.Lock()
	n := r.count
	last := r.last
	if n == 0 {
		r.mu.Unlock()
		return SampleWindow{}
	}
	buf := make([]int64, n)
	copy(buf, r.samples[:n])
	r.mu.Unlock()

	slices.Sort(buf)
	return SampleWindow{
		Count: n,
		Last:  last,
		Min:   buf[0],
		P50:   percentile(buf, 0.50),
		P95:   percentile(buf, 0.95),
		P99:   percentile(buf, 0.99),
		Max:   buf[n-1],
	}
}

// percentile returns the value at the given quantile from a pre-sorted
// slice using nearest-rank. Cheap and good enough for a 100-sample window.
func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	idx = max(idx, 0)
	idx = min(idx, len(sorted)-1)
	return sorted[idx]
}

type store struct {
	topologyBuilds         atomic.Int64
	topologyDuration       ringBuffer
	topologyNodeCount      ringBuffer
	topologyEdgeCount      ringBuffer
	topologyPayloadBytes   ringBuffer
	topologyEstimatedNodes ringBuffer

	sseBroadcasts atomic.Int64
	sseDrops      atomic.Int64
}

var global = &store{}

// RecordTopologyBuild records one topology build's duration and the
// resulting graph size, plus the estimator's pre-build node count guess
// (the same value used to drive large-cluster optimizations and debounce
// tuning — exposing it here lets us see when the estimator drifts from
// reality).
func RecordTopologyBuild(d time.Duration, nodes, edges, estimated int) {
	global.topologyBuilds.Add(1)
	global.topologyDuration.add(d.Microseconds())
	global.topologyNodeCount.add(int64(nodes))
	global.topologyEdgeCount.add(int64(edges))
	global.topologyEstimatedNodes.add(int64(estimated))
}

// RecordTopologyPayload records the marshaled byte size of one /api/topology
// response or one broadcast frame. Tracks what we actually ship over the
// wire (post-JSON encoding) — the metric most relevant to "did the frontend
// OOM on parse" bug reports.
func RecordTopologyPayload(bytes int) {
	global.topologyPayloadBytes.add(int64(bytes))
}

// IncSSEBroadcast increments the SSE broadcast counter (one per
// broadcastTopologyUpdate fire, not per client).
func IncSSEBroadcast() { global.sseBroadcasts.Add(1) }

// IncSSEDrop increments the silent-drop counter (safeSend default case).
func IncSSEDrop() { global.sseDrops.Add(1) }

// GetSSEStats returns the current SSE counters without snapshotting topology
// sample windows.
func GetSSEStats() SSEStats {
	return SSEStats{
		TotalBroadcasts: global.sseBroadcasts.Load(),
		TotalDrops:      global.sseDrops.Load(),
	}
}

// GetSnapshot returns a consistent point-in-time view of all counters
// and sample windows for inclusion in /api/diagnostics responses.
func GetSnapshot() Snapshot {
	return Snapshot{
		Topology: TopologyStats{
			TotalBuilds:    global.topologyBuilds.Load(),
			DurationUs:     global.topologyDuration.snapshot(),
			NodeCount:      global.topologyNodeCount.snapshot(),
			EdgeCount:      global.topologyEdgeCount.snapshot(),
			PayloadBytes:   global.topologyPayloadBytes.snapshot(),
			EstimatedNodes: global.topologyEstimatedNodes.snapshot(),
		},
		SSE: GetSSEStats(),
	}
}

// Reset clears all counters and windows. Intended for tests; not safe to
// call concurrently with the Record/Inc/Get functions.
func Reset() {
	global = &store{}
}
