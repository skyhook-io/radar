package timeline

import (
	"testing"
	"time"
)

// A context switch must clear the per-cluster drop records + counters (they name
// resources from the previous cluster) while preserving process uptime.
func TestResetMetricsForContextSwitch(t *testing.T) {
	m := GetMetrics()
	m.Reset() // start from a clean slate for a deterministic test
	start := m.startTime

	// Simulate activity on "cluster A": a dropped Secret names a resource.
	RecordDrop("Secret", "team-a", "db-creds", DropReasonNoisyFilter, "update", "cluster-a")
	RecordDrop("ConfigMap", "team-a", "app-cfg", DropReasonAlreadySeen, "update", "cluster-a")
	if snap := m.GetSnapshot(); len(snap.RecentDrops) != 2 {
		t.Fatalf("precondition: want 2 recent drops, got %d", len(snap.RecentDrops))
	}

	// Let a measurable amount of process time pass so we can assert uptime survives.
	time.Sleep(5 * time.Millisecond)

	ResetMetricsForContextSwitch()

	snap := m.GetSnapshot()
	if len(snap.RecentDrops) != 0 {
		t.Fatalf("RecentDrops must be cleared on context switch, got %d: %+v", len(snap.RecentDrops), snap.RecentDrops)
	}
	if len(snap.Counters.Dropped) != 0 {
		t.Fatalf("dropped counters must reset on context switch, got %+v", snap.Counters.Dropped)
	}
	if m.startTime != start {
		t.Fatalf("startTime (process uptime) must be preserved across a context switch")
	}
	if snap.Uptime == "" {
		t.Fatalf("uptime must still be reported after a context switch")
	}
}

func TestRetiredBindingDiagnostics(t *testing.T) {
	ResetStore()
	t.Cleanup(ResetStore)
	t.Cleanup(ResetMetricsForContextSwitch)
	retired := CaptureStore()
	ResetStore()
	ResetMetricsForContextSwitch()
	current := CaptureStore()

	for _, b := range []*StoreBinding{current, retired} {
		b.IncrementReceived("Pod")
		b.IncrementRecorded("Pod")
		b.RecordDrop("Pod", "shop", "web", DropReasonChannelFull, "update", "same-name")
	}
	retired.RecordDrop("Pod", "shop", "web", DropReasonStoreFailed, "delete", "same-name")

	snap := GetMetrics().GetSnapshot()
	if snap.Counters.Received["Pod"] != 1 || snap.Counters.Recorded["Pod"] != 1 ||
		snap.Counters.Dropped[DropReasonChannelFull] != 1 || len(snap.Counters.Dropped) != 1 ||
		len(snap.RecentDrops) != 1 || !LastStoreFailure("Pod").IsZero() {
		t.Fatalf("expected only the current binding's diagnostics, got %+v, last store failure %v", snap, LastStoreFailure("Pod"))
	}
}

// Drops are stamped with the cluster they came from (captured at wiring time,
// not read live), and read paths keep only the active cluster's — so a straggler
// drop recorded after a switch, stamped with the OLD cluster, is filtered out
// even though ResetMetricsForContextSwitch already ran. This closes the
// late-callback race that a reset alone leaves open.
func TestDropsForCluster_FiltersStaleClusterDrops(t *testing.T) {
	m := GetMetrics()
	m.Reset()

	RecordDrop("Secret", "team-a", "db-creds", DropReasonNoisyFilter, "update", "cluster-a")
	// Simulate a switch to cluster-b, then a STRAGGLER old-informer callback that
	// fires after the reset — it is stamped with its wiring-time cluster (a).
	ResetMetricsForContextSwitch()
	RecordDrop("Secret", "team-a", "db-creds", DropReasonNoisyFilter, "update", "cluster-a")
	// And a genuine cluster-b drop.
	RecordDrop("Pod", "team-b", "web", DropReasonAlreadySeen, "update", "cluster-b")

	all := GetMetrics().GetSnapshot().RecentDrops
	active := DropsForCluster(all, "cluster-b")
	for _, d := range active {
		if d.ClusterContext != "cluster-b" {
			t.Fatalf("active-cluster view must exclude cluster-a straggler, got %+v", d)
		}
	}
	if len(active) != 1 || active[0].Name != "web" {
		t.Fatalf("want only the cluster-b Pod drop, got %+v", active)
	}
}
