package timeline

import (
	"testing"
	"time"
)

func TestStoreBindingMetricsAcrossReset(t *testing.T) {
	ResetStore()
	t.Cleanup(ResetStore)
	old := CaptureStore()
	oldMetrics := GetMetrics()
	old.IncrementReceived("Secret")
	old.IncrementRecorded("Secret")
	CaptureStore().RecordDrop("Secret", "team-a", "db-creds", DropReasonNoisyFilter, "update", "cluster-a")
	CaptureStore().RecordDrop("ConfigMap", "team-a", "app-cfg", DropReasonAlreadySeen, "update", "cluster-a")
	old.RecordDrop("Secret", "team-a", "db-creds", DropReasonStoreFailed, "delete", "cluster-a")
	if snap := oldMetrics.GetSnapshot(); len(snap.RecentDrops) != 3 || LastStoreFailure("Secret").IsZero() {
		t.Fatalf("precondition: want 3 recent drops and a store failure, got %+v", snap)
	}

	ResetStore()
	current := CaptureStore()
	m := GetMetrics()
	minUptime := time.Since(startTime)
	fresh := m.GetSnapshot()
	maxUptime := time.Since(startTime)
	if len(fresh.RecentDrops) != 0 || len(fresh.Counters.Dropped) != 0 || len(fresh.Counters.Recorded) != 0 || len(fresh.Counters.Received) != 0 ||
		!LastStoreFailure("Secret").IsZero() {
		t.Fatalf("expected fresh metrics after ResetStore, got %+v, last store failure=%v", fresh, LastStoreFailure("Secret"))
	}
	if uptime, err := time.ParseDuration(fresh.Uptime); err != nil || uptime < minUptime || uptime > maxUptime || fresh.UptimeSec != int64(uptime.Seconds()) {
		t.Fatalf("uptime after ResetStore = %q (%ds), want process uptime between %v and %v", fresh.Uptime, fresh.UptimeSec, minUptime, maxUptime)
	}

	for _, b := range []*StoreBinding{current, old} {
		b.IncrementReceived("Pod")
		b.IncrementRecorded("Pod")
		b.RecordDrop("Pod", "shop", "web", DropReasonChannelFull, "update", "same-name")
	}
	old.RecordDrop("Pod", "shop", "web", DropReasonStoreFailed, "delete", "same-name")

	snap := GetMetrics().GetSnapshot()
	if snap.Counters.Received["Pod"] != 1 || snap.Counters.Recorded["Pod"] != 1 ||
		snap.Counters.Dropped[DropReasonChannelFull] != 1 || len(snap.Counters.Dropped) != 1 ||
		len(snap.RecentDrops) != 1 || !LastStoreFailure("Pod").IsZero() {
		t.Fatalf("expected only the current binding's diagnostics, got %+v, last store failure %v", snap, LastStoreFailure("Pod"))
	}
	if snap := oldMetrics.GetSnapshot(); snap.Counters.Received["Pod"] != 1 || snap.Counters.Recorded["Pod"] != 1 ||
		snap.Counters.Dropped[DropReasonChannelFull] != 1 || snap.Counters.Dropped[DropReasonStoreFailed] != 2 || len(snap.RecentDrops) != 5 {
		t.Fatalf("expected the old binding to retain its late writes, got %+v", snap)
	}
}

func TestDropsForCluster_FiltersStaleClusterDrops(t *testing.T) {
	ResetStore()
	t.Cleanup(ResetStore)

	CaptureStore().RecordDrop("Secret", "team-a", "db-creds", DropReasonNoisyFilter, "update", "cluster-a")
	ResetStore()
	CaptureStore().RecordDrop("Secret", "team-a", "db-creds", DropReasonNoisyFilter, "update", "cluster-a")
	// And a genuine cluster-b drop.
	CaptureStore().RecordDrop("Pod", "team-b", "web", DropReasonAlreadySeen, "update", "cluster-b")

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
