package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/kubernetes"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/k8s"
)

// Modeled on a CloudNativePG 1.27 primary's GET /pg/status.
const cnpgStatusFixture = `{
 "currentLsn": "0/7000148", "systemID": "7690724460905209884", "isPrimary": true,
 "replayPaused": false, "pendingRestart": true, "isWalReceiverActive": false,
 "pendingRestartForDecrease": true, "isPgRewindRunning": false, "instanceManagerVersion": "1.27.0",
 "mightBeUnavailable": false, "isArchivingWAL": true,
 "pod": {"metadata": {"name": "pg-orders-1"}},
 "lastArchivedWAL": "000000010000000000000006", "lastArchivedWALTime": "2026-09-29T08:24:24.875131Z",
 "lastFailedWALTime": "-infinity", "currentWAL": "000000010000000000000007", "readyWalFiles": 3,
 "timeLineID": 1,
 "replicationInfo": [{
   "applicationName": "pg-orders-2", "state": "streaming",
   "receivedLsn": "0/7000148", "writeLsn": "0/7000148", "flushLsn": "0/7000100", "replayLsn": "0/7000000",
   "writeLag": "00:00:00.012", "flushLag": "1 day 00:00:01", "replayLag": "garbage",
   "syncState": "async", "syncPriority": "0"
 }],
 "replicationSlotsInfo": [{"slotName": "_cnpg_pg_orders_2", "slotType": "physical", "restartLsn": "0/7000148", "walStatus": "reserved", "active": true}, {"slotName": "orders_sub", "plugin": "pgoutput", "slotType": "logical", "database": "app", "walStatus": "extended", "active": false}],
  "pgStatBasebackupsInfo": [
    {"usename": "streaming_replica", "application_name": "pg-orders-4-join", "backend_start": "2026-09-30T10:00:00.5Z", "phase": "streaming database files", "backup_total": 4000, "backup_streamed": 1000, "backup_total_pretty": "4000 bytes", "backup_streamed_pretty": "1000 bytes", "tablespaces_total": 1, "tablespaces_streamed": 0},
    {"usename": "streaming_replica", "application_name": "pg-orders-5-join", "backend_start": "2026-09-30T10:01:00Z", "phase": "waiting for checkpoint to finish", "backup_total": 0, "backup_streamed": 0, "tablespaces_total": 0, "tablespaces_streamed": 0}
  ]
}`

// Modeled on the CNPG 1.27 default monitoring queries. The exporter itself
// connects as postgres with application_name cnpg_metrics_exporter.
const cnpgMetricsFixture = `# HELP cnpg_backends_total Number of backends
# TYPE cnpg_backends_total gauge
cnpg_backends_total{application_name="cnpg_metrics_exporter",datname="app",state="active",usename="postgres"} 1
cnpg_backends_total{application_name="pg-orders-2",datname="",state="active",usename="streaming_replica"} 1
cnpg_backends_total{application_name="psql",datname="app",state="idle",usename="app"} 4
cnpg_backends_total{application_name="api",datname="app",state="idle in transaction",usename="app"} 2
cnpg_backends_total{application_name="bg",datname="app",state="",usename="app"} 7
# TYPE cnpg_backends_waiting_total gauge
cnpg_backends_waiting_total 1
# TYPE cnpg_pg_postmaster_start_time gauge
cnpg_pg_postmaster_start_time 1.790698960738028e+09
# TYPE cnpg_backends_max_tx_duration_seconds gauge
cnpg_backends_max_tx_duration_seconds{application_name="pg-orders-2",datname="",state="active",usename="streaming_replica"} 9000
cnpg_backends_max_tx_duration_seconds{application_name="api",datname="app",state="idle in transaction",usename="app"} 42.5
# TYPE cnpg_pg_database_size_bytes gauge
cnpg_pg_database_size_bytes{datname="app"} 7.654547e+06
cnpg_pg_database_size_bytes{datname="postgres"} 7.5e+06
# TYPE cnpg_pg_database_xid_age gauge
cnpg_pg_database_xid_age{datname="app"} 29
# TYPE cnpg_pg_database_mxid_age gauge
cnpg_pg_database_mxid_age{datname="app"} 12
cnpg_pg_database_mxid_age{datname="reports"} 400000000
# TYPE cnpg_pg_extensions_update_available gauge
cnpg_pg_extensions_update_available{datname="app",default_version="1.0",extname="plpgsql",installed_version="1.0"} 0
cnpg_pg_extensions_update_available{datname="app",default_version="3.5.1",extname="postgis",installed_version="3.4.2"} 1
# TYPE cnpg_pg_settings_setting gauge
cnpg_pg_settings_setting{name="max_connections"} 100
cnpg_pg_settings_setting{name="shared_buffers"} 16384
# TYPE cnpg_pg_stat_archiver_archived_count counter
cnpg_pg_stat_archiver_archived_count 7
# TYPE cnpg_pg_stat_archiver_failed_count counter
cnpg_pg_stat_archiver_failed_count 0
# TYPE cnpg_pg_stat_archiver_seconds_since_last_archival gauge
cnpg_pg_stat_archiver_seconds_since_last_archival 63.05
# TYPE cnpg_pg_stat_archiver_seconds_since_last_failure gauge
cnpg_pg_stat_archiver_seconds_since_last_failure -1
# TYPE cnpg_collector_pg_wal gauge
cnpg_collector_pg_wal{value="size"} 1.34217728e+08
cnpg_collector_pg_wal{value="volume_size"} NaN
# TYPE cnpg_pg_stat_database_xact_commit counter
cnpg_pg_stat_database_xact_commit{datname="app"} 100
cnpg_pg_stat_database_xact_commit{datname="postgres"} 50
# TYPE cnpg_pg_stat_database_xact_rollback counter
cnpg_pg_stat_database_xact_rollback{datname="app"} 2
# TYPE cnpg_pg_stat_database_blks_hit counter
cnpg_pg_stat_database_blks_hit{datname="app"} 900
# TYPE cnpg_pg_stat_database_blks_read counter
cnpg_pg_stat_database_blks_read{datname="app"} 100
# TYPE cnpg_pg_replication_slots_pg_wal_lsn_diff gauge
cnpg_pg_replication_slots_pg_wal_lsn_diff{database="",slot_name="_cnpg_pg_orders_2",slot_type="physical"} 16384
`

const cnpgPoolerMetricsFixture = `# TYPE cnpg_pgbouncer_pools_cl_active gauge
cnpg_pgbouncer_pools_cl_active{database="pgbouncer",user="pgbouncer"} 1
cnpg_pgbouncer_pools_cl_active{database="app",user="app"} 5
cnpg_pgbouncer_pools_cl_active{database="app",user="cnpg_pooler_pgbouncer"} 1
# TYPE cnpg_pgbouncer_pools_cl_waiting gauge
cnpg_pgbouncer_pools_cl_waiting{database="app",user="app"} 2
# TYPE cnpg_pgbouncer_pools_sv_active gauge
cnpg_pgbouncer_pools_sv_active{database="app",user="app"} 3
# TYPE cnpg_pgbouncer_pools_sv_idle gauge
cnpg_pgbouncer_pools_sv_idle{database="app",user="app"} 1
# TYPE cnpg_pgbouncer_pools_sv_used gauge
cnpg_pgbouncer_pools_sv_used{database="app",user="app"} 0
# TYPE cnpg_pgbouncer_pools_maxwait gauge
cnpg_pgbouncer_pools_maxwait{database="app",user="app"} 1
# TYPE cnpg_pgbouncer_pools_maxwait_us gauge
cnpg_pgbouncer_pools_maxwait_us{database="app",user="app"} 250000
`

func cnpgRuntimeWithUID(u *unstructured.Unstructured, uid string) *unstructured.Unstructured {
	u.SetUID(types.UID(uid))
	return u
}

func cnpgFptr(v float64) *float64 { return &v }

func cnpgEqF(p *float64, want float64) bool { return p != nil && *p == want }

func TestParseCNPGPgStatus(t *testing.T) {
	facts, partial, err := parseCNPGPgStatus([]byte(cnpgStatusFixture))
	if err != nil || partial != "" {
		t.Fatalf("parse: %v %q", err, partial)
	}
	if !facts.IsPrimary || !facts.PendingRestart || facts.CurrentLsn != "0/7000148" || facts.Timeline == nil || *facts.Timeline != 1 {
		t.Errorf("facts = %+v", facts)
	}
	if !facts.PendingRestartForDecrease || facts.InstanceManagerVersion != "1.27.0" || facts.RoleDetail != "primary" {
		t.Errorf("instance facts = %+v", facts)
	}
	a := facts.Archiving
	if a.LastArchivedWal != "000000010000000000000006" || a.LastArchivedAt == "" || a.LastFailedAt != "" || a.ReadyWalFiles == nil || *a.ReadyWalFiles != 3 {
		t.Errorf("archiving = %+v, want -infinity omitted", a)
	}
	if len(facts.Replication) != 1 {
		t.Fatalf("replication = %+v", facts.Replication)
	}
	rep := facts.Replication[0]
	if !cnpgEqF(rep.WriteLag, 0.012) || !cnpgEqF(rep.FlushLag, 86401) || rep.ReplayLag != nil || rep.ReplayLagRaw != "garbage" {
		t.Errorf("lags = %v %v %v raw %q", rep.WriteLag, rep.FlushLag, rep.ReplayLag, rep.ReplayLagRaw)
	}
	if rep.SyncPriority == nil || *rep.SyncPriority != 0 || rep.SentLsn != "0/7000148" || rep.FlushLsn != "0/7000100" {
		t.Errorf("replication = %+v", rep)
	}
	if len(facts.Slots) != 2 || facts.Slots[0].Name != "_cnpg_pg_orders_2" || !facts.Slots[0].Active {
		t.Errorf("slots = %+v", facts.Slots)
	}
	if l := facts.Slots[1]; l.Type != "logical" || l.Plugin != "pgoutput" || l.Database != "app" || l.Active {
		t.Errorf("logical slot = %+v", l)
	}
	if len(facts.BaseBackups) != 2 {
		t.Fatalf("base backups = %+v", facts.BaseBackups)
	}
	bb := facts.BaseBackups[0]
	if bb.Instance != "pg-orders-4" || bb.Phase != "streaming database files" || bb.TotalBytes == nil || *bb.TotalBytes != 4000 || bb.StreamedBytes != 1000 || bb.StartedAt != "2026-09-30T10:00:00.5Z" {
		t.Errorf("base backup = %+v", bb)
	}
	if facts.BaseBackups[1].TotalBytes != nil {
		t.Errorf("an unestimated total must stay unknown, got %v", *facts.BaseBackups[1].TotalBytes)
	}
	none, _, err := parseCNPGPgStatus([]byte(`{"isPrimary": true}`))
	if err != nil || none.BaseBackups == nil || len(none.BaseBackups) != 0 {
		t.Errorf("a readable report without base backups must say none, got %+v %v", none, err)
	}

	for _, bad := range []string{`not json`, `{"pod":{}}`, `<html>proxy error</html>`} {
		if _, _, err := parseCNPGPgStatus([]byte(bad)); err == nil {
			t.Errorf("%q parsed as a status report", bad)
		}
	}
}

func TestCNPGRoleDetail(t *testing.T) {
	for want, f := range map[string]CNPGInstanceStatusFacts{
		"primary":      {IsPrimary: true, IsWalReceiverActive: true},
		"pgRewind":     {IsPgRewindRunning: true, IsWalReceiverActive: true},
		"replayPaused": {ReplayPaused: true, IsWalReceiverActive: true},
		"streaming":    {IsWalReceiverActive: true},
		"fileBased":    {},
	} {
		if got := cnpgRoleDetail(&f); got != want {
			t.Errorf("cnpgRoleDetail(%+v) = %q, want %q", f, got, want)
		}
	}
}

func TestParseCNPGPgInterval(t *testing.T) {
	for in, want := range map[string]*float64{
		"00:00:00":              cnpgFptr(0),
		"00:00:01.5":            cnpgFptr(1.5),
		"-00:00:02":             cnpgFptr(-2),
		"1 day 02:03:04":        cnpgFptr(86400 + 2*3600 + 3*60 + 4),
		"2 days":                cnpgFptr(172800),
		"1 mon 3 days 00:00:01": cnpgFptr(30*86400 + 3*86400 + 1),
		"":                      nil,
		"soon":                  nil,
		"1 fortnight":           nil,
	} {
		got := parseCNPGPgInterval(in)
		if (got == nil) != (want == nil) || (got != nil && *got != *want) {
			t.Errorf("parseCNPGPgInterval(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestCNPGInstanceMetricFacts(t *testing.T) {
	samples, err := parseCNPGPromSamples([]byte(cnpgMetricsFixture))
	if err != nil {
		t.Fatal(err)
	}
	facts, capped := cnpgInstanceMetricFacts(samples)
	if capped != "" {
		t.Errorf("capped = %q", capped)
	}
	// Platform sessions (the exporter, replication) and state '' rows are out.
	if !cnpgEqF(facts.SessionsTotal, 6) || len(facts.Sessions) != 2 {
		t.Fatalf("sessions = %+v total %v", facts.Sessions, facts.SessionsTotal)
	}
	for _, s := range facts.Sessions {
		if s.User == "streaming_replica" || s.Application == cnpgMetricsExporterApp || s.State == "" {
			t.Errorf("platform or stateless row leaked: %+v", s)
		}
	}
	if facts.Sessions[0].State != "idle" || facts.Sessions[0].Count != 4 {
		t.Errorf("sessions not sorted by count: %+v", facts.Sessions)
	}
	if !cnpgEqF(facts.OldestXactSeconds, 42.5) {
		t.Errorf("oldestXact = %v, want the replication session's 9000 ignored", facts.OldestXactSeconds)
	}
	if !cnpgEqF(facts.MaxConnections, 100) || !cnpgEqF(facts.WaitingBackends, 1) || !cnpgEqF(facts.WalBytes, 134217728) {
		t.Errorf("scalars = %v %v %v", facts.MaxConnections, facts.WaitingBackends, facts.WalBytes)
	}
	if !cnpgEqF(facts.XactCommitTotal, 150) || !cnpgEqF(facts.XactRollbackTotal, 2) || !cnpgEqF(facts.BlksHit, 900) || !cnpgEqF(facts.BlksRead, 100) {
		t.Errorf("counters = %v %v %v %v", facts.XactCommitTotal, facts.XactRollbackTotal, facts.BlksHit, facts.BlksRead)
	}
	if !cnpgEqF(facts.PostmasterStartTime, 1.790698960738028e+09) {
		t.Errorf("postmaster start = %v", facts.PostmasterStartTime)
	}
	if facts.DeadlocksTotal != nil {
		t.Errorf("deadlocks = %v, want absent (family not reported)", *facts.DeadlocksTotal)
	}
	if strings.Join(facts.Missing, ",") != "cnpg_pg_stat_database_deadlocks" {
		t.Errorf("missing = %v", facts.Missing)
	}
	a := facts.Archiver
	if a == nil || !cnpgEqF(a.ArchivedCount, 7) || !cnpgEqF(a.FailedCount, 0) || !cnpgEqF(a.SecondsSinceLastArchival, 63.05) || a.SecondsSinceLastFailure != nil {
		t.Errorf("archiver = %+v, want -1 (never failed) omitted", a)
	}
	if len(facts.DatabaseSizes) != 2 || facts.DatabaseSizes[0].Database != "app" || len(facts.XidAge) != 1 {
		t.Errorf("databases = %+v xid = %+v", facts.DatabaseSizes, facts.XidAge)
	}
	if len(facts.MxidAge) != 2 || facts.MxidAge[0].Database != "reports" || facts.MxidAge[0].Age != 400000000 {
		t.Errorf("mxid = %+v, want oldest first", facts.MxidAge)
	}
	if len(facts.ExtensionUpdates) != 1 || facts.ExtensionUpdates[0] != (CNPGExtensionUpdate{Database: "app", Extension: "postgis", InstalledVersion: "3.4.2", DefaultVersion: "3.5.1"}) {
		t.Errorf("extension updates = %+v, want only postgis", facts.ExtensionUpdates)
	}
	noExt, _ := cnpgInstanceMetricFacts(map[string][]cnpgSample{"cnpg_pg_extensions_update_available": {{labels: map[string]string{"extname": "plpgsql"}, value: 0}}})
	if noExt.ExtensionUpdates == nil || len(noExt.ExtensionUpdates) != 0 {
		t.Errorf("exported with none to update must be empty, not unknown: %+v", noExt.ExtensionUpdates)
	}
	if unk, _ := cnpgInstanceMetricFacts(map[string][]cnpgSample{}); unk.ExtensionUpdates != nil {
		t.Errorf("unexported family must stay unknown: %+v", unk.ExtensionUpdates)
	}
	if len(facts.ReplicationSlotsRetainedBytes) != 1 || facts.ReplicationSlotsRetainedBytes[0].Bytes != 16384 {
		t.Errorf("slot retention = %+v", facts.ReplicationSlotsRetainedBytes)
	}

	// An exporter with the default queries replaced reports nothing it lacks.
	empty, _ := parseCNPGPromSamples([]byte("# TYPE custom_metric gauge\ncustom_metric 1\n"))
	bare, _ := cnpgInstanceMetricFacts(empty)
	b, _ := json.Marshal(bare)
	for _, field := range []string{"sessionsTotal", "maxConnections", "xactCommitTotal", "archiver", "oldestXactSeconds"} {
		if strings.Contains(string(b), `"`+field+`"`) {
			t.Errorf("absent measurement %s serialized: %s", field, b)
		}
	}
	if len(bare.Missing) != len(cnpgExpectedInstanceFamilies) {
		t.Errorf("missing = %v", bare.Missing)
	}
}

func TestCNPGSessionRowsAreCapped(t *testing.T) {
	var b strings.Builder
	b.WriteString("# TYPE cnpg_backends_total gauge\n")
	for i := 0; i < cnpgRuntimeMaxRows+5; i++ {
		fmt.Fprintf(&b, "cnpg_backends_total{application_name=\"a%d\",datname=\"app\",state=\"idle\",usename=\"app\"} 1\n", i)
	}
	samples, err := parseCNPGPromSamples([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	facts, capped := cnpgInstanceMetricFacts(samples)
	if capped == "" || len(facts.Sessions) != cnpgRuntimeMaxRows || !cnpgEqF(facts.SessionsTotal, float64(cnpgRuntimeMaxRows+5)) {
		t.Errorf("capped=%q rows=%d total=%v, want capped rows with the full total", capped, len(facts.Sessions), facts.SessionsTotal)
	}
}

func TestCNPGPromTextTruncatedIsPartial(t *testing.T) {
	cut := strings.Index(cnpgMetricsFixture, "cnpg_pg_stat_database_xact_commit{datname=\"postgres\"}")
	out := cnpgProxyOutcome{state: cnpgRuntimeStateOK, body: []byte(cnpgMetricsFixture[:cut+10]), truncated: true}
	got := cnpgInstanceMetricsFrom(out)
	if got.State != cnpgRuntimeStatePartial || got.Reason == "" {
		t.Fatalf("state = %q reason %q", got.State, got.Reason)
	}
	// The family on the cut line may continue past the cap, so it must not
	// read as a complete total.
	if got.XactCommitTotal != nil {
		t.Errorf("xactCommitTotal = %v from a family cut at the cap", *got.XactCommitTotal)
	}
	if !cnpgEqF(got.MaxConnections, 100) {
		t.Errorf("families before the cut should survive: %+v", got.CNPGInstanceMetricFacts)
	}
}

func TestCNPGPoolerFacts(t *testing.T) {
	samples, err := parseCNPGPromSamples([]byte(cnpgPoolerMetricsFixture))
	if err != nil {
		t.Fatal(err)
	}
	facts, _ := cnpgPoolerFacts(samples)
	if len(facts.Pools) != 1 {
		t.Fatalf("pools = %+v, want the admin and auth_query pools excluded", facts.Pools)
	}
	p := facts.Pools[0]
	if p.Database != "app" || !cnpgEqF(p.ClActive, 5) || !cnpgEqF(p.ClWaiting, 2) || !cnpgEqF(p.SvActive, 3) || !cnpgEqF(p.SvIdle, 1) || !cnpgEqF(p.SvUsed, 0) || !cnpgEqF(p.MaxwaitSeconds, 1.25) {
		t.Errorf("pool = %+v", p)
	}
	if len(facts.Missing) != 0 {
		t.Errorf("missing = %v", facts.Missing)
	}
}

// cnpgFakeProxyAPIServer stands in for the apiserver's pods/proxy. The handler
// decides per (scheme, pod, port, path); every request is recorded.
type cnpgFakeProxyAPIServer struct {
	mu       sync.Mutex
	requests []string
	headers  []http.Header
}

func (f *cnpgFakeProxyAPIServer) record(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	f.headers = append(f.headers, r.Header.Clone())
}

func (f *cnpgFakeProxyAPIServer) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

type cnpgProxyCall struct{ scheme, pod, port, path string }

func useCNPGProxyAPIServer(t *testing.T, handle func(w http.ResponseWriter, c cnpgProxyCall)) *cnpgFakeProxyAPIServer {
	t.Helper()
	f := &cnpgFakeProxyAPIServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/selfsubjectaccessreviews") {
			var review authv1.SelfSubjectAccessReview
			_ = json.NewDecoder(r.Body).Decode(&review)
			review.Status.Allowed = true
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(review)
			return
		}
		f.record(r)
		// /api/v1/namespaces/{ns}/pods/{scheme:pod:port}/proxy/{path}
		parts := strings.SplitN(r.URL.Path, "/", 9)
		if len(parts) < 8 || parts[5] != "pods" || parts[7] != "proxy" {
			http.NotFound(w, r)
			return
		}
		target := strings.Split(parts[6], ":")
		path := "/"
		if len(parts) == 9 {
			path += parts[8]
		}
		handle(w, cnpgProxyCall{scheme: target[0], pod: target[1], port: target[2], path: path})
	}))
	t.Cleanup(srv.Close)
	previous := k8s.SetTestConfig(&rest.Config{Host: srv.URL})
	t.Cleanup(func() { k8s.SetTestConfig(previous) })
	client, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	previousClient := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previousClient) })
	cnpgLocalCanIMu.Lock()
	cnpgLocalCanIMemo = map[string]cnpgLocalCanIEntry{}
	cnpgLocalCanIMu.Unlock()
	resetCNPGRuntimeMemo(t)
	return f
}

func resetCNPGRuntimeMemo(t *testing.T) {
	t.Helper()
	clear := func() {
		cnpgRuntimeMemoMu.Lock()
		cnpgRuntimeMemoEntries = map[string]cnpgRuntimeMemoEntry{}
		cnpgRuntimeMemoMu.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

func cnpgWriteAPIStatus(w http.ResponseWriter, code int, reason metav1.StatusReason, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(metav1.Status{
		TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
		Status:   metav1.StatusFailure, Reason: reason, Code: int32(code), Message: msg,
	})
}

func seedCNPGRuntimeCluster(t *testing.T, ns string) {
	t.Helper()
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds,
		cnpgRuntimeWithUID(cnpgObj("postgresql.cnpg.io/v1", "Cluster", ns, "pg-orders", map[string]any{"instances": int64(2)}, nil), ns+"-uid"),
	)
	owner := metav1.OwnerReference{APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: "pg-orders", UID: types.UID(ns + "-uid"), Controller: boolPtr(true)}
	primary := cnpgPod(ns, "pg-orders-1", "pg-orders", owner)
	primary.UID = types.UID(ns + "-p1")
	primary.Spec.Containers[0].Command = []string{"/controller/manager", "instance", "run", "--status-port-tls"}
	replica := cnpgPod(ns, "pg-orders-2", "pg-orders", owner)
	replica.UID = types.UID(ns + "-p2")
	replica.Labels["cnpg.io/instanceRole"] = "replica"
	impostor := cnpgPod(ns, "pg-orders-impostor", "pg-orders")
	seedCNPGPods(t, primary, replica, impostor)
}

func getCNPGRuntime(t *testing.T, path string) (int, CNPGClusterRuntimeResponse, string) {
	t.Helper()
	resp, err := http.Get(testServer.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var out CNPGClusterRuntimeResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("decode: %v (%s)", err, body)
		}
	}
	return resp.StatusCode, out, string(body)
}

// cnpgHealthyInstances answers like a real cluster: the primary's status port
// speaks TLS (a plain request is a bare 400), everything else is plain.
func cnpgHealthyInstances(w http.ResponseWriter, c cnpgProxyCall) {
	switch {
	case c.port == "8000" && c.pod == "pg-orders-1" && c.scheme == "http":
		w.WriteHeader(http.StatusBadRequest)
	case c.port == "8000" && c.path == cnpgStatusPath:
		status := cnpgStatusFixture
		if c.pod == "pg-orders-2" {
			status = `{"isPrimary": false, "pod": {}, "receivedLsn": "0/7000148", "replayLsn": "0/7000148", "isWalReceiverActive": true, "lastFailedWALTime": "-infinity"}`
		}
		_, _ = io.WriteString(w, status)
	case c.port == "9187" && c.scheme == "http" && c.path == cnpgMetricsPath:
		_, _ = io.WriteString(w, cnpgMetricsFixture)
	default:
		http.Error(w, "unexpected "+c.scheme+":"+c.pod+":"+c.port+c.path, http.StatusTeapot)
	}
}

func TestCNPGClusterRuntime_OK(t *testing.T) {
	seedCNPGRuntimeCluster(t, "pgrt")
	api := useCNPGProxyAPIServer(t, cnpgHealthyInstances)

	status, got, body := getCNPGRuntime(t, "/api/cnpg/clusters/pgrt/pg-orders/runtime")
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	if got.Cluster.UID != "pgrt-uid" || got.SampledAt == "" || got.Permission.Proxy != "allowed" || got.Permission.Grant != "get pods/proxy in pgrt" {
		t.Errorf("envelope = %+v", got)
	}
	if len(got.Instances) != 2 || got.Instances[0].Pod != "pg-orders-1" || got.Instances[1].Pod != "pg-orders-2" {
		t.Fatalf("instances = %+v, want only the owned instances", got.Instances)
	}
	p, r := got.Instances[0], got.Instances[1]
	if p.Role != "primary" || r.Role != "replica" {
		t.Errorf("roles = %s %s", p.Role, r.Role)
	}
	if p.Status.State != cnpgRuntimeStateOK || p.Status.Scheme != "https" || p.Status.CapturedAt == "" || !p.Status.IsPrimary {
		t.Errorf("primary status = %+v", p.Status.CNPGRuntimeSource)
	}
	if p.Metrics.State != cnpgRuntimeStateOK || p.Metrics.Scheme != "http" || !cnpgEqF(p.Metrics.SessionsTotal, 6) {
		t.Errorf("primary metrics = %+v", p.Metrics.CNPGRuntimeSource)
	}
	if len(p.Status.Slots) != 2 || !cnpgEqF(p.Status.Slots[0].RetainedBytes, 16384) {
		t.Errorf("slot retention not joined from the same instance: %+v", p.Status.Slots)
	}
	if r.Status.State != cnpgRuntimeStateOK || r.Status.IsPrimary || !r.Status.IsWalReceiverActive || r.Status.Scheme != "http" {
		t.Errorf("replica status = %+v", r.Status)
	}
	for _, req := range api.seen() {
		if strings.Contains(req, "impostor") {
			t.Errorf("proxied to a non-instance Pod: %s", req)
		}
		if !strings.HasPrefix(req, "GET ") {
			t.Errorf("non-GET proxy request: %s", req)
		}
	}
	for _, h := range api.headers {
		if h.Get("Cookie") != "" || h.Get("X-Forwarded-User") != "" {
			t.Errorf("browser headers forwarded: %v", h)
		}
	}

	// Within the memo lifetime a second view reuses the answers.
	before := len(api.seen())
	if status, _, body := getCNPGRuntime(t, "/api/cnpg/clusters/pgrt/pg-orders/runtime"); status != http.StatusOK {
		t.Fatalf("second read: %d %s", status, body)
	}
	if after := len(api.seen()); after != before {
		t.Errorf("second read issued %d more proxy requests, want 0 (memoized)", after-before)
	}
}

func TestCNPGClusterRuntime_ProxyDeniedIsPerSource(t *testing.T) {
	seedCNPGRuntimeCluster(t, "pgrtdeny")
	api := useCNPGProxyAPIServer(t, func(w http.ResponseWriter, c cnpgProxyCall) {
		cnpgWriteAPIStatus(w, http.StatusForbidden, metav1.StatusReasonForbidden,
			fmt.Sprintf(`pods "%s:%s" is forbidden: User "alice" cannot get resource "pods/proxy" in API group "" in the namespace "pgrtdeny"`, c.pod, c.port))
	})
	status, got, body := getCNPGRuntime(t, "/api/cnpg/clusters/pgrtdeny/pg-orders/runtime")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 so Kubernetes facts still render: %s", status, body)
	}
	if got.Permission.Proxy != "denied" {
		t.Errorf("permission = %+v", got.Permission)
	}
	for _, inst := range got.Instances {
		if inst.Status.State != cnpgRuntimeStateDenied || inst.Metrics.State != cnpgRuntimeStateDenied {
			t.Errorf("%s: states = %s/%s, want denied", inst.Pod, inst.Status.State, inst.Metrics.State)
		}
		if inst.Status.CNPGInstanceStatusFacts != nil || inst.Metrics.CNPGInstanceMetricFacts != nil {
			t.Errorf("%s: facts on a denied source", inst.Pod)
		}
	}
	// A denial is not a scheme problem: one request per source, no retry.
	if n := len(api.seen()); n != 4 {
		t.Errorf("proxy requests = %d, want 4 (no scheme retry on forbidden)", n)
	}
	if strings.Contains(body, `"isPrimary"`) || strings.Contains(body, `"sessionsTotal"`) {
		t.Errorf("denied sources serialized facts: %s", body)
	}
}

func TestCNPGClusterRuntime_UnreachableAndRedirect(t *testing.T) {
	seedCNPGRuntimeCluster(t, "pgrtdown")
	api := useCNPGProxyAPIServer(t, func(w http.ResponseWriter, c cnpgProxyCall) {
		switch {
		case c.path == "/pg/archive/partial":
			_, _ = io.WriteString(w, "archived")
		case c.port == "8000":
			// The instance manager redirecting to a state-changing path must
			// not be followed.
			w.Header().Set("Location", "/api/v1/namespaces/pgrtdown/pods/"+c.scheme+":"+c.pod+":8000/proxy/pg/archive/partial")
			w.WriteHeader(http.StatusFound)
		default:
			cnpgWriteAPIStatus(w, http.StatusServiceUnavailable, metav1.StatusReasonServiceUnavailable,
				"error trying to reach service: dial tcp 10.0.0.5:9187: connect: connection refused")
		}
	})
	status, got, body := getCNPGRuntime(t, "/api/cnpg/clusters/pgrtdown/pg-orders/runtime")
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	for _, inst := range got.Instances {
		if inst.Metrics.State != cnpgRuntimeStateUnreachable || inst.Metrics.Error != "nothing is listening on port 9187 in the Pod" {
			t.Errorf("%s metrics = %+v", inst.Pod, inst.Metrics.CNPGRuntimeSource)
		}
		if inst.Status.State != cnpgRuntimeStateError || !strings.Contains(inst.Status.Error, "redirect") {
			t.Errorf("%s status = %+v", inst.Pod, inst.Status.CNPGRuntimeSource)
		}
	}
	for _, req := range api.seen() {
		if strings.Contains(req, "/pg/archive/partial") {
			t.Fatalf("followed a redirect to %s", req)
		}
	}
	// connection refused is not a scheme mismatch: no second attempt.
	metricsCalls := 0
	for _, req := range api.seen() {
		if strings.Contains(req, ":9187/") {
			metricsCalls++
		}
	}
	if metricsCalls != 2 {
		t.Errorf("metrics proxy requests = %d, want one per instance", metricsCalls)
	}
}

func TestCNPGClusterRuntime_ResponseCaps(t *testing.T) {
	seedCNPGRuntimeCluster(t, "pgrtcap")
	useCNPGProxyAPIServer(t, cnpgHealthyInstances)
	prevStatus, prevMetrics := cnpgRuntimeStatusCap, cnpgRuntimeMetricsCap
	cnpgRuntimeStatusCap, cnpgRuntimeMetricsCap = 64, int64(strings.Index(cnpgMetricsFixture, "# TYPE cnpg_pg_stat_database_xact_commit"))
	t.Cleanup(func() { cnpgRuntimeStatusCap, cnpgRuntimeMetricsCap = prevStatus, prevMetrics })

	_, got, body := getCNPGRuntime(t, "/api/cnpg/clusters/pgrtcap/pg-orders/runtime")
	p := got.Instances[0]
	if p.Status.State != cnpgRuntimeStateError || !strings.Contains(p.Status.Error, "larger than") {
		t.Errorf("status over cap = %+v", p.Status.CNPGRuntimeSource)
	}
	if p.Metrics.State != cnpgRuntimeStatePartial || p.Metrics.Reason == "" || p.Metrics.XactCommitTotal != nil {
		t.Errorf("metrics over cap = %+v: %s", p.Metrics.CNPGRuntimeSource, body)
	}
}

func TestCNPGClusterRuntime_NotFoundAndEmpty(t *testing.T) {
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds,
		cnpgRuntimeWithUID(cnpgObj("postgresql.cnpg.io/v1", "Cluster", "pgrtempty", "pg-new", nil, nil), "new-uid"),
	)
	useCNPGProxyAPIServer(t, cnpgHealthyInstances)
	if status, _, _ := getCNPGRuntime(t, "/api/cnpg/clusters/pgrtempty/missing/runtime"); status != http.StatusNotFound {
		t.Errorf("missing cluster: %d, want 404", status)
	}
	status, got, body := getCNPGRuntime(t, "/api/cnpg/clusters/pgrtempty/pg-new/runtime")
	if status != http.StatusOK || got.Instances == nil || len(got.Instances) != 0 {
		t.Errorf("no instances: %d %s", status, body)
	}
}

func TestCNPGClusterRuntime_FencedExplained(t *testing.T) {
	cluster := cnpgRuntimeWithUID(cnpgObj("postgresql.cnpg.io/v1", "Cluster", "pgrtfence", "pg-orders", nil, nil), "pgrtfence-uid")
	cluster.SetAnnotations(map[string]string{cnpgFencedAnnotation: `["pg-orders-1"]`})
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds, cluster)
	owner := metav1.OwnerReference{APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: "pg-orders", UID: "pgrtfence-uid", Controller: boolPtr(true)}
	seedCNPGPods(t, cnpgPod("pgrtfence", "pg-orders-1", "pg-orders", owner))
	useCNPGProxyAPIServer(t, func(w http.ResponseWriter, c cnpgProxyCall) {
		cnpgWriteAPIStatus(w, http.StatusServiceUnavailable, metav1.StatusReasonServiceUnavailable, "error trying to reach service: connection refused")
	})
	_, got, body := getCNPGRuntime(t, "/api/cnpg/clusters/pgrtfence/pg-orders/runtime")
	if len(got.Instances) != 1 || !got.Instances[0].Fenced || !strings.Contains(got.Instances[0].Metrics.Error, "fenced") {
		t.Errorf("fenced instance = %s", body)
	}
}

func seedCNPGPoolerChain(t *testing.T, ns string, poolerUID types.UID) {
	t.Helper()
	ctx := context.Background()
	deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: "pg-orders-rw", Namespace: ns, UID: types.UID(ns + "-deploy"),
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "postgresql.cnpg.io/v1", Kind: "Pooler", Name: "pg-orders-rw", UID: poolerUID, Controller: boolPtr(true)}},
	}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Name: "pg-orders-rw-abc", Namespace: ns, UID: types.UID(ns + "-rs"),
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "pg-orders-rw", UID: deploy.UID, Controller: boolPtr(true)}},
	}}
	strayRS := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "stray", Namespace: ns, UID: types.UID(ns + "-stray")}}
	if _, err := testFakeClient.AppsV1().Deployments(ns).Create(ctx, deploy, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, r := range []*appsv1.ReplicaSet{rs, strayRS} {
		if _, err := testFakeClient.AppsV1().ReplicaSets(ns).Create(ctx, r, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_ = testFakeClient.AppsV1().Deployments(ns).Delete(context.Background(), deploy.Name, metav1.DeleteOptions{})
		_ = testFakeClient.AppsV1().ReplicaSets(ns).Delete(context.Background(), rs.Name, metav1.DeleteOptions{})
		_ = testFakeClient.AppsV1().ReplicaSets(ns).Delete(context.Background(), strayRS.Name, metav1.DeleteOptions{})
	})
	poolerPod := func(name string, owner metav1.OwnerReference) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID(ns + "-" + name),
				Labels: map[string]string{cnpgPoolerNameLabel: "pg-orders-rw"}, OwnerReferences: []metav1.OwnerReference{owner}},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "pgbouncer"}}},
		}
	}
	cache := k8s.GetResourceCache()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, e1 := cache.Deployments().Deployments(ns).Get(deploy.Name)
		_, e2 := cache.ReplicaSets().ReplicaSets(ns).Get(rs.Name)
		_, e3 := cache.ReplicaSets().ReplicaSets(ns).Get(strayRS.Name)
		if e1 == nil && e2 == nil && e3 == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pooler chain never reached the cache")
		}
		time.Sleep(20 * time.Millisecond)
	}
	seedCNPGPods(t,
		poolerPod("pg-orders-rw-abc-1", metav1.OwnerReference{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: rs.Name, UID: rs.UID, Controller: boolPtr(true)}),
		poolerPod("pg-orders-rw-stray", metav1.OwnerReference{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: strayRS.Name, UID: strayRS.UID, Controller: boolPtr(true)}),
	)
}

func TestCNPGPoolerRuntime(t *testing.T) {
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds,
		cnpgRuntimeWithUID(cnpgObj("postgresql.cnpg.io/v1", "Pooler", "pgrtpool", "pg-orders-rw", map[string]any{"cluster": map[string]any{"name": "pg-orders"}}, nil), "pooler-uid"),
	)
	seedCNPGPoolerChain(t, "pgrtpool", "pooler-uid")
	api := useCNPGProxyAPIServer(t, func(w http.ResponseWriter, c cnpgProxyCall) {
		if c.port != "9127" || c.path != cnpgMetricsPath || c.scheme != "http" {
			http.Error(w, "unexpected", http.StatusTeapot)
			return
		}
		_, _ = io.WriteString(w, cnpgPoolerMetricsFixture)
	})

	resp, err := http.Get(testServer.URL + "/api/cnpg/poolers/pgrtpool/pg-orders-rw/runtime")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	var got CNPGPoolerRuntimeResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Pooler.UID != "pooler-uid" || len(got.Pods) != 1 || got.Pods[0].Pod != "pg-orders-rw-abc-1" {
		t.Fatalf("pods = %s, want only the Pod on the Pooler's controller chain", body)
	}
	pod := got.Pods[0]
	if pod.State != cnpgRuntimeStateOK || pod.CNPGPoolerPodFacts == nil || len(pod.Pools) != 1 || !cnpgEqF(pod.Pools[0].ClWaiting, 2) {
		t.Errorf("pod = %s", body)
	}
	for _, req := range api.seen() {
		if strings.Contains(req, "stray") {
			t.Errorf("proxied to a Pod off the controller chain: %s", req)
		}
	}
	r2, _ := http.Get(testServer.URL + "/api/cnpg/poolers/pgrtpool/missing/runtime")
	r2.Body.Close()
	if r2.StatusCode != http.StatusNotFound {
		t.Errorf("missing pooler: %d, want 404", r2.StatusCode)
	}
}

func TestClassifyCNPGProxyErrorSchemeMismatchOnly(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		msg      string
		mismatch bool
	}{
		{"error trying to reach service: tls: first record does not look like a TLS handshake", true},
		{"error trying to reach service: http: server gave HTTP response to HTTPS client", true},
		{"error trying to reach service: tls: failed to verify certificate: x509: certificate signed by unknown authority", false},
		{"error trying to reach service: dial tcp 10.0.0.5:9187: connect: connection refused", false},
	} {
		out := classifyCNPGProxyError(ctx, fmt.Errorf("%s", c.msg), cnpgProxyOutcome{})
		if out.schemeMismatch != c.mismatch {
			t.Errorf("%q: schemeMismatch = %v, want %v", c.msg, out.schemeMismatch, c.mismatch)
		}
	}
	timeoutCtx, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancel()
	if out := classifyCNPGProxyError(timeoutCtx, context.DeadlineExceeded, cnpgProxyOutcome{}); out.schemeMismatch || out.state != cnpgRuntimeStateUnreachable {
		t.Errorf("timeout = %+v, want unreachable without retry", out)
	}
}

func TestParseCNPGPgStatusIncompleteReportIsNotNone(t *testing.T) {
	masked := `{"isPrimary": false, "mightBeUnavailable": true, "mightBeUnavailableMaskedError": "failed to connect to /controller/run/.s.PGSQL.5432", "instanceManagerVersion": "1.27.0"}`
	facts, partial, err := parseCNPGPgStatus([]byte(masked))
	if err != nil {
		t.Fatal(err)
	}
	if !facts.Incomplete || facts.MaskedError == "" || !strings.Contains(partial, "masked an error") {
		t.Errorf("incomplete not reported: %+v %q", facts, partial)
	}
	if facts.Replication != nil || facts.Slots != nil || facts.BaseBackups != nil || facts.Archiving != nil {
		t.Errorf("unread lists must be unknown, not empty: repl=%v slots=%v bb=%v arch=%v", facts.Replication, facts.Slots, facts.BaseBackups, facts.Archiving)
	}
	if facts.RoleDetail != "" {
		t.Errorf("a standby's role detail is not established from a masked report, got %q", facts.RoleDetail)
	}
	body, _ := json.Marshal(facts)
	if !strings.Contains(string(body), `"baseBackups":null`) || strings.Contains(string(body), `"baseBackups":[]`) {
		t.Errorf("wire form = %s", body)
	}

	withRows := `{"isPrimary": true, "mightBeUnavailable": true, "mightBeUnavailableMaskedError": "x", "replicationSlotsInfo": [{"slotName": "s", "slotType": "logical", "active": true}]}`
	f2, _, _ := parseCNPGPgStatus([]byte(withRows))
	if len(f2.Slots) != 1 || f2.Replication != nil || f2.RoleDetail != "primary" {
		t.Errorf("a list the report did fill is whole; the rest unknown: %+v", f2)
	}

	rewind, partial, _ := parseCNPGPgStatus([]byte(`{"isPrimary": false, "isPgRewindRunning": true}`))
	if !rewind.Incomplete || rewind.RoleDetail != "pgRewind" || !strings.Contains(partial, "pg_rewind") {
		t.Errorf("pg_rewind report = %+v %q", rewind, partial)
	}
}

func TestCNPGDatabaseStatsAndCheckpoints(t *testing.T) {
	body := `# TYPE cnpg_pg_stat_database_xact_commit counter
cnpg_pg_stat_database_xact_commit{datname="app"} 90
cnpg_pg_stat_database_xact_commit{datname="reports"} 10
cnpg_pg_stat_database_xact_commit{datname=""} 5
# TYPE cnpg_pg_stat_database_xact_rollback counter
cnpg_pg_stat_database_xact_rollback{datname="app"} 10
# TYPE cnpg_pg_stat_database_temp_files counter
cnpg_pg_stat_database_temp_files{datname="app"} 3
# TYPE cnpg_pg_stat_database_temp_bytes counter
cnpg_pg_stat_database_temp_bytes{datname="app"} 4096
cnpg_pg_stat_database_temp_bytes{datname="reports"} 1024
# TYPE cnpg_pg_stat_checkpointer_checkpoints_timed counter
cnpg_pg_stat_checkpointer_checkpoints_timed 7
# TYPE cnpg_pg_stat_checkpointer_checkpoints_req counter
cnpg_pg_stat_checkpointer_checkpoints_req 2
# TYPE cnpg_pg_stat_checkpointer_restartpoints_timed counter
cnpg_pg_stat_checkpointer_restartpoints_timed 4
# TYPE cnpg_pg_stat_checkpointer_restartpoints_done counter
cnpg_pg_stat_checkpointer_restartpoints_done 3
# TYPE cnpg_pg_stat_checkpointer_buffers_written counter
cnpg_pg_stat_checkpointer_buffers_written 1234
`
	samples, err := parseCNPGPromSamples([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	facts, _ := cnpgInstanceMetricFacts(samples)
	if len(facts.Databases) != 2 || facts.Databases[0].Database != "app" || facts.Databases[1].Database != "reports" {
		t.Fatalf("databases = %+v, want app and reports without the shared-objects row", facts.Databases)
	}
	app := facts.Databases[0]
	if !cnpgEqF(app.XactCommit, 90) || !cnpgEqF(app.XactRollback, 10) || !cnpgEqF(app.TempFiles, 3) || !cnpgEqF(app.TempBytes, 4096) || app.Deadlocks != nil {
		t.Errorf("app = %+v", app)
	}
	if facts.Databases[1].XactRollback != nil {
		t.Error("a counter the exporter did not report must stay unknown")
	}
	if !cnpgEqF(facts.TempBytesTotal, 5120) {
		t.Errorf("temp bytes total = %v", facts.TempBytesTotal)
	}
	c := facts.Checkpoints
	if c == nil || c.Source != "pg_stat_checkpointer" || !cnpgEqF(c.Timed, 7) || !cnpgEqF(c.Requested, 2) || !cnpgEqF(c.RestartpointsTimed, 4) || c.RestartpointsRequested != nil || !cnpgEqF(c.BuffersWritten, 1234) {
		t.Errorf("checkpoints = %+v", c)
	}

	old, _ := parseCNPGPromSamples([]byte("# TYPE cnpg_pg_stat_bgwriter_checkpoints_timed counter\ncnpg_pg_stat_bgwriter_checkpoints_timed 5\n# TYPE cnpg_pg_stat_bgwriter_buffers_checkpoint counter\ncnpg_pg_stat_bgwriter_buffers_checkpoint 99\n"))
	of, _ := cnpgInstanceMetricFacts(old)
	if of.Checkpoints == nil || of.Checkpoints.Source != "pg_stat_bgwriter" || !cnpgEqF(of.Checkpoints.BuffersWritten, 99) || of.Checkpoints.RestartpointsTimed != nil {
		t.Errorf("pre-17 checkpoints = %+v", of.Checkpoints)
	}
	if none, _ := cnpgInstanceMetricFacts(map[string][]cnpgSample{}); none.Checkpoints != nil || none.Databases != nil {
		t.Error("absent families must stay unknown")
	}
}

func TestCNPGMetricsGenerationAndSessionsByState(t *testing.T) {
	samples, err := parseCNPGPromSamples([]byte(cnpgMetricsFixture + "# TYPE cnpg_last_update_timestamp gauge\ncnpg_last_update_timestamp 1.79e+09\n"))
	if err != nil {
		t.Fatal(err)
	}
	facts, _ := cnpgInstanceMetricFacts(samples)
	if !cnpgEqF(facts.LastUpdateTimestamp, 1.79e9) {
		t.Errorf("generation = %v", facts.LastUpdateTimestamp)
	}
	sum := 0.0
	for _, v := range facts.SessionsByState {
		sum += v
	}
	if facts.SessionsByState["idle"] != 4 || sum != *facts.SessionsTotal {
		t.Errorf("by state = %v, total %v", facts.SessionsByState, *facts.SessionsTotal)
	}
	if old, _ := cnpgInstanceMetricFacts(map[string][]cnpgSample{}); old.LastUpdateTimestamp != nil || old.SessionsByState != nil {
		t.Error("absent families must stay unknown")
	}
}

func TestCNPGMemoizedReadOutlivesTheCallerThatStartedIt(t *testing.T) {
	target := cnpgProxyTarget{namespace: "pg", pod: "pg-1", podUID: types.UID(fmt.Sprint("uid-memo-detach-", time.Now().UnixNano())), port: cnpgStatusPort, path: cnpgStatusPath, scheme: "https"}
	type key struct{}
	started := make(chan struct{})
	release := make(chan struct{})
	fetch := func(ctx context.Context) string {
		close(started)
		<-release
		if ctx.Err() != nil {
			return "cancelled"
		}
		if ctx.Value(key{}) != "alice" {
			return "identity lost"
		}
		return "ok"
	}
	first, cancelFirst := context.WithCancel(context.WithValue(context.Background(), key{}, "alice"))
	done := make(chan string, 2)
	go func() { done <- cnpgMemoized(first, "id-detach", target, time.Minute, fetch) }()
	<-started
	// A second caller joins the in-flight read, then the first hangs up.
	go func() {
		done <- cnpgMemoized(context.WithValue(context.Background(), key{}, "alice"), "id-detach", target, time.Minute, func(context.Context) string { return "second fetch" })
	}()
	time.Sleep(20 * time.Millisecond)
	cancelFirst()
	close(release)
	for i := 0; i < 2; i++ {
		if got := <-done; got != "ok" {
			t.Errorf("caller %d got %q, want the shared read's answer", i, got)
		}
	}
	if got := cnpgMemoized(context.Background(), "id-detach", target, time.Minute, func(context.Context) string { return "refetched" }); got != "ok" {
		t.Errorf("memo = %q, want the completed read cached", got)
	}
}

func TestCNPGRuntimeRunnerKeepsItsCapWhenTheCallerGoes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	run := newCNPGRuntimeRunner(ctx)
	var running, maxRunning, ran int32
	release := make(chan struct{})
	admitted := make(chan struct{}, 32)
	for i := 0; i < 12; i++ {
		run.do(func(context.Context) {
			n := atomic.AddInt32(&running, 1)
			for {
				m := atomic.LoadInt32(&maxRunning)
				if n <= m || atomic.CompareAndSwapInt32(&maxRunning, m, n) {
					break
				}
			}
			atomic.AddInt32(&ran, 1)
			admitted <- struct{}{}
			<-release
			atomic.AddInt32(&running, -1)
		})
	}
	for i := 0; i < cnpgRuntimeConcurrency; i++ {
		<-admitted
	}
	cancel()
	time.Sleep(20 * time.Millisecond)
	close(release)
	run.wait()
	if maxRunning > int32(cnpgRuntimeConcurrency) {
		t.Errorf("max concurrent reads = %d, want ≤ %d", maxRunning, cnpgRuntimeConcurrency)
	}
	if ran != int32(cnpgRuntimeConcurrency) {
		t.Errorf("reads run = %d, want only the %d admitted before the caller left", ran, cnpgRuntimeConcurrency)
	}
}

func TestCNPGMemoizedDoesNotKeepATimeout(t *testing.T) {
	target := cnpgProxyTarget{namespace: "pg", pod: "pg-1", podUID: types.UID(fmt.Sprint("uid-memo-timeout-", time.Now().UnixNano())), port: cnpgStatusPort, path: cnpgStatusPath, scheme: "https"}
	calls := 0
	fetch := func(ctx context.Context) string {
		calls++
		cnpgMarkTimedOut(ctx, fmt.Errorf("proxy: %w", context.DeadlineExceeded))
		return "timed out"
	}
	cnpgMemoized(context.Background(), "id-timeout", target, time.Minute, fetch)
	cnpgMemoized(context.Background(), "id-timeout", target, time.Minute, fetch)
	if calls != 2 {
		t.Errorf("fetches = %d, want 2: a timed-out read must not be memoized", calls)
	}
	ok := cnpgProxyTarget{namespace: "pg", pod: "pg-1", podUID: types.UID(fmt.Sprint("uid-memo-ok-", time.Now().UnixNano())), port: cnpgStatusPort, path: cnpgStatusPath, scheme: "https"}
	n := 0
	good := func(context.Context) string { n++; return "ok" }
	cnpgMemoized(context.Background(), "id-timeout", ok, time.Minute, good)
	cnpgMemoized(context.Background(), "id-timeout", ok, time.Minute, good)
	if n != 1 {
		t.Errorf("fetches = %d, want 1 for a read that answered", n)
	}
}

func TestCNPGRuntimeRunnerRunsNothingForACancelledCaller(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	run := newCNPGRuntimeRunner(ctx)
	var ran int32
	for i := 0; i < 100; i++ {
		run.do(func(context.Context) { atomic.AddInt32(&ran, 1) })
	}
	run.wait()
	if ran != 0 {
		t.Errorf("%d reads ran for a caller that had already left, want 0", ran)
	}
}

func TestCNPGMarkTimedOutIgnoresTheWordInNames(t *testing.T) {
	flagFor := func(err error) bool {
		f := new(atomic.Bool)
		cnpgMarkTimedOut(context.WithValue(context.Background(), cnpgReadTimedOutKey{}, f), err)
		return f.Load()
	}
	named := apierrors.NewGenericServerResponse(500, "get", schema.GroupResource{Resource: "pods"}, "https:timeout-demo-1:8000", "pq: out of shared memory", 0, true)
	if flagFor(named) {
		t.Error("a Pod named timeout-demo-1 was read as a timeout")
	}
	if flagFor(errors.New(`Get "https://api/namespaces/timeout-demo/pods/timeout-demo-1/proxy": EOF`)) {
		t.Error("a URL containing \"timeout\" was read as a timeout")
	}
	for _, err := range []error{
		fmt.Errorf("x: %w", context.DeadlineExceeded),
		apierrors.NewTimeoutError("slow", 0),
		&net.DNSError{IsTimeout: true},
	} {
		if !flagFor(err) {
			t.Errorf("%v was not read as a timeout", err)
		}
	}
}
