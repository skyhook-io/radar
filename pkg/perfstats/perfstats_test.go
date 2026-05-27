package perfstats

import (
	"testing"
	"time"
)

func TestRingBufferPercentiles(t *testing.T) {
	Reset()
	for i := 1; i <= 50; i++ {
		RecordTopologyBuild(time.Duration(i)*time.Microsecond, i*10, i*20, i)
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
		RecordTopologyBuild(time.Duration(i)*time.Microsecond, 0, 0, 0)
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
