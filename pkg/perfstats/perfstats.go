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

// changeSampleEvery throttles ring appends on the resource-change path, which
// can run at ~1k/s on a large cluster. The high-water mark is still updated on
// every change; only the distribution is sampled.
const changeSampleEvery = 100

// BuildKind distinguishes the three shapes of topology build by the scope they
// cover, which is what drives their cost. Without it a single duration window
// averages a multi-second cluster-wide build together with a 20ms
// namespace-scoped one, and the resulting p95 describes neither.
type BuildKind string

const (
	// BuildFull covers every namespace — no namespace filter was applied.
	BuildFull BuildKind = "full"
	// BuildScoped is bounded by a namespace filter.
	BuildScoped BuildKind = "scoped"
	// BuildRefused is a build the large-cluster guard declined to run.
	BuildRefused BuildKind = "refused"
)

var buildKinds = [...]BuildKind{BuildFull, BuildScoped, BuildRefused}

func (k BuildKind) index() int {
	switch k {
	case BuildFull:
		return 0
	case BuildRefused:
		return 2
	default:
		return 1
	}
}

// Snapshot is the rendered view of all counters + sample windows.
type Snapshot struct {
	Topology          TopologyStats          `json:"topology"`
	TopologyByKind    TopologyKindStats      `json:"topologyByKind"`
	SSE               SSEStats               `json:"sse"`
	SSECycle          SSECycleStats          `json:"sseCycle"`
	Changes           ChangeStats            `json:"changes"`
	RelationshipCache RelationshipCacheStats `json:"relationshipCache"`
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

// TopologyKindStats splits the build stats by BuildKind. A fixed struct rather
// than a map so JSON key order — and therefore the diagnostics markdown — is
// deterministic across snapshots.
type TopologyKindStats struct {
	Full    TopologyKindWindow `json:"full"`
	Scoped  TopologyKindWindow `json:"scoped"`
	Refused TopologyKindWindow `json:"refused"`
}

// TopologyKindWindow is TopologyStats minus PayloadBytes. Payload size is
// recorded where a graph is marshaled, which is downstream of the build and
// carries no record of which kind produced it — so a per-kind payload field
// could only ever report zero, and a zero that means "never measured" is worse
// than an absent field.
type TopologyKindWindow struct {
	TotalBuilds    int64        `json:"totalBuilds"`
	DurationUs     SampleWindow `json:"durationUs"`
	NodeCount      SampleWindow `json:"nodeCount"`
	EdgeCount      SampleWindow `json:"edgeCount"`
	EstimatedNodes SampleWindow `json:"estimatedNodes"`
}

// SSEStats covers the SSE broadcaster's counters. Kept free of sample windows
// so the Prometheus collector can read it on every scrape without snapshotting
// (and sorting) any ring buffers.
type SSEStats struct {
	TotalBroadcasts int64 `json:"totalBroadcasts"`
	TotalDrops      int64 `json:"totalDrops"`
	Abandoned       int64 `json:"abandoned"`
	Coalesced       int64 `json:"coalesced"`
	Retries         int64 `json:"retries"`
	DebounceMs      int64 `json:"debounceMs"`
}

// SSECycleStats describes the cost and fan-out of one broadcast cycle. AuthGroups
// is the multiplier that matters: each one is an independent clone + strip +
// marshal of the whole graph, so a slow cycle is as often "we did it 40 times"
// as it is "the build was slow".
type SSECycleStats struct {
	CycleDurationUs SampleWindow `json:"cycleDurationUs"`
	ClientGroups    SampleWindow `json:"clientGroups"`
	AuthGroups      SampleWindow `json:"authGroups"`
	MarshalUs       SampleWindow `json:"marshalUs"`
}

// ChangeStats tracks the resource-change channel feeding the SSE watcher.
// Drops are already recorded downstream once the channel overflows; depth is
// what shows the approach to that cliff, and Received/uptime gives the rate.
type ChangeStats struct {
	Received   int64        `json:"received"`
	QueueDepth SampleWindow `json:"queueDepth"`
	QueueCap   int          `json:"queueCap"`
	HighWater  int64        `json:"highWater"`
}

// RelationshipCacheStats covers full topology builds that run on a request
// goroutine because the cached graph was dirty — the shape that made resource
// detail views block for seconds on large clusters.
type RelationshipCacheStats struct {
	OnDemandRebuilds  int64        `json:"onDemandRebuilds"`
	OnDemandRebuildUs SampleWindow `json:"onDemandRebuildUs"`
	IndexBuilds       int64        `json:"indexBuilds"`
	IndexBuildUs      SampleWindow `json:"indexBuildUs"`
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

// topologyRings is one build shape's sample windows. The aggregate and each
// BuildKind get their own instance.
type topologyRings struct {
	builds         atomic.Int64
	duration       ringBuffer
	nodeCount      ringBuffer
	edgeCount      ringBuffer
	payloadBytes   ringBuffer
	estimatedNodes ringBuffer
}

func (t *topologyRings) record(d time.Duration, nodes, edges, estimated int) {
	t.builds.Add(1)
	t.duration.add(d.Microseconds())
	t.nodeCount.add(int64(nodes))
	t.edgeCount.add(int64(edges))
	t.estimatedNodes.add(int64(estimated))
}

func (t *topologyRings) kindSnapshot() TopologyKindWindow {
	return TopologyKindWindow{
		TotalBuilds:    t.builds.Load(),
		DurationUs:     t.duration.snapshot(),
		NodeCount:      t.nodeCount.snapshot(),
		EdgeCount:      t.edgeCount.snapshot(),
		EstimatedNodes: t.estimatedNodes.snapshot(),
	}
}

func (t *topologyRings) snapshot() TopologyStats {
	return TopologyStats{
		TotalBuilds:    t.builds.Load(),
		DurationUs:     t.duration.snapshot(),
		NodeCount:      t.nodeCount.snapshot(),
		EdgeCount:      t.edgeCount.snapshot(),
		PayloadBytes:   t.payloadBytes.snapshot(),
		EstimatedNodes: t.estimatedNodes.snapshot(),
	}
}

type store struct {
	topology       topologyRings
	topologyByKind [len(buildKinds)]topologyRings

	sseBroadcasts atomic.Int64
	sseDrops      atomic.Int64
	sseAbandoned  atomic.Int64
	sseCoalesced  atomic.Int64
	sseRetries    atomic.Int64
	sseDebounceMs atomic.Int64

	sseCycleDuration ringBuffer
	sseClientGroups  ringBuffer
	sseAuthGroups    ringBuffer
	sseMarshal       ringBuffer

	changesReceived  atomic.Int64
	changeQueueDepth ringBuffer
	changeQueueCap   atomic.Int64
	changeHighWater  atomic.Int64

	relRebuilds  atomic.Int64
	relRebuildUs ringBuffer
	relIndexes   atomic.Int64
	relIndexUs   ringBuffer
}

var global = &store{}

// RecordTopologyBuild records one topology build's duration and the
// resulting graph size, plus the estimator's pre-build node count guess
// (the same value used to drive large-cluster optimizations and debounce
// tuning — exposing it here lets us see when the estimator drifts from
// reality). Recorded twice: once into the aggregate, once into the window
// for its BuildKind.
func RecordTopologyBuild(kind BuildKind, d time.Duration, nodes, edges, estimated int) {
	global.topology.record(d, nodes, edges, estimated)
	global.topologyByKind[kind.index()].record(d, nodes, edges, estimated)
}

// RecordTopologyPayload records the marshaled byte size of one /api/topology
// response or one broadcast frame. Tracks what we actually ship over the
// wire (post-JSON encoding) — the metric most relevant to "did the frontend
// OOM on parse" bug reports.
func RecordTopologyPayload(bytes int) {
	global.topology.payloadBytes.add(int64(bytes))
}

// IncSSEBroadcast increments the SSE broadcast counter (one per
// broadcastTopologyUpdate fire, not per client).
func IncSSEBroadcast() { global.sseBroadcasts.Add(1) }

// IncSSEDrop increments the silent-drop counter (safeSend default case).
func IncSSEDrop() { global.sseDrops.Add(1) }

// IncSSEAbandoned counts cycles thrown away because the cluster changed
// mid-build. Work that was done and never reached a client.
func IncSSEAbandoned() { global.sseAbandoned.Add(1) }

// IncSSECoalesced counts broadcast requests that folded into an already-queued
// one. Persistently high means the worker never catches up with the change rate.
func IncSSECoalesced() { global.sseCoalesced.Add(1) }

// IncSSERetry counts triggers the worker re-armed after finding the cluster
// view torn down.
func IncSSERetry() { global.sseRetries.Add(1) }

// SetSSEDebounce records the debounce interval currently in force, so a report
// shows which rung of the ladder the cluster settled on.
func SetSSEDebounce(d time.Duration) { global.sseDebounceMs.Store(d.Milliseconds()) }

// RecordBroadcastCycle records the cost and fan-out of one broadcast cycle.
// clientGroups counts distinct namespace/RBAC/view-mode groups (one graph build
// each); authGroups counts the provider-permission subgroups across all of them
// (one clone + strip + marshal each).
func RecordBroadcastCycle(d time.Duration, clientGroups, authGroups int, marshal time.Duration) {
	global.sseCycleDuration.add(d.Microseconds())
	global.sseClientGroups.add(int64(clientGroups))
	global.sseAuthGroups.add(int64(authGroups))
	global.sseMarshal.add(marshal.Microseconds())
}

// RecordChangeReceived samples the resource-change channel as the SSE watcher
// drains it. The high-water mark is updated on every call; the distribution is
// sampled every changeSampleEvery-th call to keep a ~1k/s path lock-free most
// of the time.
func RecordChangeReceived(depth, capacity int) {
	n := global.changesReceived.Add(1)
	global.changeQueueCap.Store(int64(capacity))

	d := int64(depth)
	for {
		hw := global.changeHighWater.Load()
		if d <= hw || global.changeHighWater.CompareAndSwap(hw, d) {
			break
		}
	}

	if n%changeSampleEvery == 0 {
		global.changeQueueDepth.add(d)
	}
}

// RecordRelationshipRebuild records a full topology rebuild that ran because a
// reader found the relationship cache dirty — i.e. on a request goroutine.
func RecordRelationshipRebuild(d time.Duration) {
	global.relRebuilds.Add(1)
	global.relRebuildUs.add(d.Microseconds())
}

// RecordRelationshipIndex records one IndexByResource build (O(edges)).
func RecordRelationshipIndex(d time.Duration) {
	global.relIndexes.Add(1)
	global.relIndexUs.add(d.Microseconds())
}

// GetSSEStats returns the current SSE counters without snapshotting any
// sample windows.
func GetSSEStats() SSEStats {
	return SSEStats{
		TotalBroadcasts: global.sseBroadcasts.Load(),
		TotalDrops:      global.sseDrops.Load(),
		Abandoned:       global.sseAbandoned.Load(),
		Coalesced:       global.sseCoalesced.Load(),
		Retries:         global.sseRetries.Load(),
		DebounceMs:      global.sseDebounceMs.Load(),
	}
}

// GetSnapshot returns a consistent point-in-time view of all counters
// and sample windows for inclusion in /api/diagnostics responses.
func GetSnapshot() Snapshot {
	return Snapshot{
		Topology: global.topology.snapshot(),
		TopologyByKind: TopologyKindStats{
			Full:    global.topologyByKind[BuildFull.index()].kindSnapshot(),
			Scoped:  global.topologyByKind[BuildScoped.index()].kindSnapshot(),
			Refused: global.topologyByKind[BuildRefused.index()].kindSnapshot(),
		},
		SSE: GetSSEStats(),
		SSECycle: SSECycleStats{
			CycleDurationUs: global.sseCycleDuration.snapshot(),
			ClientGroups:    global.sseClientGroups.snapshot(),
			AuthGroups:      global.sseAuthGroups.snapshot(),
			MarshalUs:       global.sseMarshal.snapshot(),
		},
		Changes: ChangeStats{
			Received:   global.changesReceived.Load(),
			QueueDepth: global.changeQueueDepth.snapshot(),
			QueueCap:   int(global.changeQueueCap.Load()),
			HighWater:  global.changeHighWater.Load(),
		},
		RelationshipCache: RelationshipCacheStats{
			OnDemandRebuilds:  global.relRebuilds.Load(),
			OnDemandRebuildUs: global.relRebuildUs.snapshot(),
			IndexBuilds:       global.relIndexes.Load(),
			IndexBuildUs:      global.relIndexUs.snapshot(),
		},
	}
}

// Reset clears all counters and windows. Intended for tests; not safe to
// call concurrently with the Record/Inc/Get functions.
func Reset() {
	global = &store{}
}
