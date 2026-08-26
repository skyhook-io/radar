package perfstats

import (
	"testing"
	"time"
)

func TestRingBufferPercentiles(t *testing.T) {
	Reset()
	for i := 1; i <= 50; i++ {
		RecordTopologyBuild(BuildScoped, time.Duration(i)*time.Microsecond, i*10, i*20, i)
	}
	snap := GetSnapshot()
	if snap.Topology.TotalBuilds != 50 {
		t.Errorf("TotalBuilds = %d, want 50", snap.Topology.TotalBuilds)
	}
	if snap.Topology.DurationUs.Count != 50 {
		t.Errorf("DurationUs.Count = %d, want 50", snap.Topology.DurationUs.Count)
	}
	if snap.Topology.DurationUs.Last != 50 {
		t.Errorf("DurationUs.Last = %d, want 50", snap.Topology.DurationUs.Last)
	}
	if snap.Topology.DurationUs.Min != 1 {
		t.Errorf("DurationUs.Min = %d, want 1", snap.Topology.DurationUs.Min)
	}
	if snap.Topology.DurationUs.Max != 50 {
		t.Errorf("DurationUs.Max = %d, want 50", snap.Topology.DurationUs.Max)
	}
	// P50 over [1..50]: nearest-rank gives sorted[int(49*0.5)] = sorted[24] = 25
	if snap.Topology.DurationUs.P50 != 25 {
		t.Errorf("DurationUs.P50 = %d, want 25", snap.Topology.DurationUs.P50)
	}
	// P95: sorted[int(49*0.95)] = sorted[46] = 47
	if snap.Topology.DurationUs.P95 != 47 {
		t.Errorf("DurationUs.P95 = %d, want 47", snap.Topology.DurationUs.P95)
	}
}

func TestRingBufferWrapsAt100(t *testing.T) {
	Reset()
	for i := 1; i <= 250; i++ {
		RecordTopologyBuild(BuildScoped, time.Duration(i)*time.Microsecond, 0, 0, 0)
	}
	snap := GetSnapshot()
	if snap.Topology.TotalBuilds != 250 {
		t.Errorf("TotalBuilds = %d, want 250", snap.Topology.TotalBuilds)
	}
	if snap.Topology.DurationUs.Count != 100 {
		t.Errorf("DurationUs.Count = %d, want 100", snap.Topology.DurationUs.Count)
	}
	// Window should hold samples 151..250 (the last 100).
	if snap.Topology.DurationUs.Min != 151 {
		t.Errorf("DurationUs.Min = %d, want 151", snap.Topology.DurationUs.Min)
	}
	if snap.Topology.DurationUs.Max != 250 {
		t.Errorf("DurationUs.Max = %d, want 250", snap.Topology.DurationUs.Max)
	}
	if snap.Topology.DurationUs.Last != 250 {
		t.Errorf("DurationUs.Last = %d, want 250", snap.Topology.DurationUs.Last)
	}
}

func TestEmptyWindow(t *testing.T) {
	Reset()
	snap := GetSnapshot()
	if snap.Topology.DurationUs.Count != 0 {
		t.Errorf("expected empty window, got count %d", snap.Topology.DurationUs.Count)
	}
	if snap.Topology.TotalBuilds != 0 {
		t.Errorf("expected 0 builds, got %d", snap.Topology.TotalBuilds)
	}
}

func TestSSECounters(t *testing.T) {
	Reset()
	IncSSEBroadcast()
	IncSSEBroadcast()
	IncSSEBroadcast()
	IncSSEDrop()
	stats := GetSSEStats()
	if stats.TotalBroadcasts != 3 {
		t.Errorf("TotalBroadcasts = %d, want 3", stats.TotalBroadcasts)
	}
	if stats.TotalDrops != 1 {
		t.Errorf("TotalDrops = %d, want 1", stats.TotalDrops)
	}

	snap := GetSnapshot()
	if snap.SSE != stats {
		t.Errorf("GetSnapshot().SSE = %+v, want %+v", snap.SSE, stats)
	}
}

func TestBuildKindsAreIsolated(t *testing.T) {
	Reset()
	RecordTopologyBuild(BuildFull, 12*time.Second, 4000, 9000, 4200)
	RecordTopologyBuild(BuildScoped, 20*time.Millisecond, 80, 120, 90)
	RecordTopologyBuild(BuildScoped, 30*time.Millisecond, 90, 130, 95)
	RecordTopologyBuild(BuildRefused, time.Millisecond, 0, 0, 60000)

	snap := GetSnapshot()
	if snap.Topology.TotalBuilds != 4 {
		t.Errorf("aggregate TotalBuilds = %d, want 4", snap.Topology.TotalBuilds)
	}
	if got := snap.TopologyByKind.Full.TotalBuilds; got != 1 {
		t.Errorf("full TotalBuilds = %d, want 1", got)
	}
	if got := snap.TopologyByKind.Scoped.TotalBuilds; got != 2 {
		t.Errorf("scoped TotalBuilds = %d, want 2", got)
	}
	if got := snap.TopologyByKind.Refused.TotalBuilds; got != 1 {
		t.Errorf("refused TotalBuilds = %d, want 1", got)
	}
	// The whole point of the split: a 12s full build must not be averaged into
	// the scoped window, where it would hide behind two 20ms samples.
	if got := snap.TopologyByKind.Full.DurationUs.Max; got != 12_000_000 {
		t.Errorf("full max = %dus, want 12000000", got)
	}
	if got := snap.TopologyByKind.Scoped.DurationUs.Max; got != 30_000 {
		t.Errorf("scoped max = %dus, want 30000", got)
	}
	if got := snap.TopologyByKind.Refused.EstimatedNodes.Last; got != 60000 {
		t.Errorf("refused estimatedNodes = %d, want 60000", got)
	}
}

func TestChangeQueueHighWaterTracksEveryCall(t *testing.T) {
	Reset()
	// The spike lands between ring samples: it must still be visible.
	for i := 1; i <= changeSampleEvery; i++ {
		depth := 5
		if i == changeSampleEvery/2 {
			depth = 9800
		}
		RecordChangeReceived(depth, 10000)
	}

	snap := GetSnapshot()
	if snap.Changes.Received != int64(changeSampleEvery) {
		t.Errorf("Received = %d, want %d", snap.Changes.Received, changeSampleEvery)
	}
	if snap.Changes.HighWater != 9800 {
		t.Errorf("HighWater = %d, want 9800 (a spike between ring samples must still register)", snap.Changes.HighWater)
	}
	if snap.Changes.QueueCap != 10000 {
		t.Errorf("QueueCap = %d, want 10000", snap.Changes.QueueCap)
	}
	if snap.Changes.QueueDepth.Count != 1 {
		t.Errorf("QueueDepth.Count = %d, want 1 sample per %d changes", snap.Changes.QueueDepth.Count, changeSampleEvery)
	}
}

func TestBroadcastCycleAndRelationshipStats(t *testing.T) {
	Reset()
	RecordBroadcastCycle(4*time.Second, 3, 11, 900*time.Millisecond)
	RecordRelationshipRebuild(8 * time.Second)
	RecordRelationshipIndex(90 * time.Millisecond)
	IncSSEAbandoned()
	IncSSECoalesced()
	IncSSECoalesced()
	IncSSERetry()
	SetSSEDebounce(15 * time.Second)

	snap := GetSnapshot()
	if got := snap.SSECycle.CycleDurationUs.Last; got != 4_000_000 {
		t.Errorf("CycleDurationUs.Last = %d, want 4000000", got)
	}
	// authGroups > clientGroups is the fan-out multiplier we could not see before.
	if snap.SSECycle.ClientGroups.Last != 3 || snap.SSECycle.AuthGroups.Last != 11 {
		t.Errorf("groups = %d client / %d auth, want 3 / 11", snap.SSECycle.ClientGroups.Last, snap.SSECycle.AuthGroups.Last)
	}
	if got := snap.SSECycle.MarshalUs.Last; got != 900_000 {
		t.Errorf("MarshalUs.Last = %d, want 900000", got)
	}
	if snap.SSE.Abandoned != 1 || snap.SSE.Coalesced != 2 || snap.SSE.Retries != 1 {
		t.Errorf("counters = %+v, want abandoned 1 / coalesced 2 / retries 1", snap.SSE)
	}
	if snap.SSE.DebounceMs != 15000 {
		t.Errorf("DebounceMs = %d, want 15000", snap.SSE.DebounceMs)
	}
	if snap.RelationshipCache.OnDemandRebuilds != 1 || snap.RelationshipCache.OnDemandRebuildUs.Last != 8_000_000 {
		t.Errorf("rebuild stats = %+v, want 1 rebuild of 8s", snap.RelationshipCache)
	}
	if snap.RelationshipCache.IndexBuilds != 1 || snap.RelationshipCache.IndexBuildUs.Last != 90_000 {
		t.Errorf("index stats = %+v, want 1 index of 90ms", snap.RelationshipCache)
	}
}
