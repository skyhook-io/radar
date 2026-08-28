package server

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/loadtest"
	"k8s.io/client-go/kubernetes/fake"
)

// TestMeasureConsumerStallPerDebounceFire measures what the resource-change
// consumer pays at a debounce fire, before and after the topology worker.
//
// Before, the consumer called broadcastTopologyUpdate directly, so it paid a
// full topology build and drained nothing for the duration. After, it hands the
// work to the worker and returns. Both costs are measured here against the same
// census and the same build, so the comparison is measured rather than
// projected, and the derived arrival count uses the #1303 diagnostics rates:
// 236,775 changes over 261s (~907/s) into the 10,000-slot change channel.
//
// Whether those arrivals overflow depends on how long the build takes, which is
// why the count is derived rather than asserted: indexing Jobs by namespace
// (#1408) cut the build enough that they now fit. The stall itself is the thing
// under test — no change is delivered while it lasts, overflow or not.
//
// Scale defaults low enough for CI, where the build is milliseconds and the
// derived arrival count is a handful of changes — at that scale this is a smoke
// test that the comparison still runs, not a measurement of anything.
// RADAR_CENSUS_SCALE=1.0 reproduces the reported census (628k objects) and the
// numbers in the commit message; it is slow and memory hungry.
func TestMeasureConsumerStallPerDebounceFire(t *testing.T) {
	if testing.Short() {
		t.Skip("measurement test; runs full topology builds")
	}

	scale := 0.05
	if v := os.Getenv("RADAR_CENSUS_SCALE"); v != "" {
		parsed, err := strconv.ParseFloat(v, 64)
		if err != nil {
			t.Fatalf("RADAR_CENSUS_SCALE=%q: %v", v, err)
		}
		scale = parsed
	}

	const (
		changesPerSec = 907   // 236,775 changes / 261s uptime
		channelSlots  = 10000 // k8score change channel capacity
	)

	census := loadtest.LargeMultiTenantEKS.Scale(scale)
	client := fake.NewClientset(loadtest.CensusObjects(census, "registry.example/app:v1")...)
	useTestResourceCache(t, client)

	// What the consumer used to pay per debounce fire: a full build, inline.
	start := time.Now()
	topo, err := buildFullTopology()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	buildCost := time.Since(start)

	// What it pays now: a handoff to the worker. Measured with the worker busy,
	// which is the case that used to serialize builds behind each other.
	b := NewSSEBroadcaster()
	release := blockWorker(t, b)
	defer release()

	start = time.Now()
	b.requestTopologyBroadcast()
	handoffCost := time.Since(start)

	stalledChanges := int(buildCost.Seconds() * changesPerSec)
	dropped := max(stalledChanges-channelSlots, 0)

	t.Logf("census scale %.3f: %d objects, topology %d nodes / %d edges",
		scale, census.Total(), len(topo.Nodes), len(topo.Edges))
	t.Logf("consumer stall per debounce fire: inline build %v -> worker handoff %v (%.0fx faster)",
		buildCost.Round(time.Millisecond), handoffCost.Round(time.Microsecond),
		float64(buildCost)/float64(max(handoffCost, time.Nanosecond)))
	t.Logf("at %d changes/s: %d changes arrive during an inline build, %d overflow the %d-slot channel and are dropped; "+
		"with the worker the consumer keeps draining throughout",
		changesPerSec, stalledChanges, dropped, channelSlots)

	// Ratio assertion, not a wall-clock budget, so this holds on any machine.
	if handoffCost*100 > buildCost {
		t.Errorf("worker handoff %v is not decisively cheaper than a %v build; "+
			"the consumer is still paying build-scale cost per debounce fire", handoffCost, buildCost)
	}
}
