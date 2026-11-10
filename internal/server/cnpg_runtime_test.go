package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/auth"
	cnpgsvc "github.com/skyhook-io/radar/internal/cnpg"
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

func cnpgEqF(p *float64, want float64) bool { return p != nil && *p == want }

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
	localCanIMu.Lock()
	localCanIMemo = map[string]localCanIEntry{}
	localCanIMu.Unlock()
	return f
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

func getCNPGRuntime(t *testing.T, path string) (int, cnpgsvc.CNPGClusterRuntimeResponse, string) {
	t.Helper()
	resp, err := http.Get(testServer.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var out cnpgsvc.CNPGClusterRuntimeResponse
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
	case c.port == "8000" && c.path == "/pg/status":
		status := cnpgStatusFixture
		if c.pod == "pg-orders-2" {
			status = `{"isPrimary": false, "pod": {}, "receivedLsn": "0/7000148", "replayLsn": "0/7000148", "isWalReceiverActive": true, "lastFailedWALTime": "-infinity"}`
		}
		_, _ = io.WriteString(w, status)
	case c.port == "9187" && c.scheme == "http" && c.path == "/metrics":
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
	if got.Cluster.UID != "pgrt-uid" || got.SampledAt == "" || got.Permission.Proxy != "allowed" || got.Permission.Grant == nil || *got.Permission.Grant != (auth.Grant{Verb: "get", Resource: "pods", Subresource: "proxy"}).In("pgrt") {
		t.Errorf("envelope = %+v", got)
	}
	if len(got.Instances) != 2 || got.Instances[0].Pod != "pg-orders-1" || got.Instances[1].Pod != "pg-orders-2" {
		t.Fatalf("instances = %+v, want only the owned instances", got.Instances)
	}
	p, r := got.Instances[0], got.Instances[1]
	if p.Role != "primary" || r.Role != "replica" {
		t.Errorf("roles = %s %s", p.Role, r.Role)
	}
	if p.Status.State != "ok" || p.Status.Scheme != "https" || p.Status.CapturedAt == "" || !p.Status.IsPrimary {
		t.Errorf("primary status = %+v", p.Status.CNPGRuntimeSource)
	}
	if p.Metrics.State != "ok" || p.Metrics.Scheme != "http" || !cnpgEqF(p.Metrics.SessionsTotal, 6) {
		t.Errorf("primary metrics = %+v", p.Metrics.CNPGRuntimeSource)
	}
	if len(p.Status.Slots) != 2 || !cnpgEqF(p.Status.Slots[0].RetainedBytes, 16384) {
		t.Errorf("slot retention not joined from the same instance: %+v", p.Status.Slots)
	}
	if r.Status.State != "ok" || r.Status.IsPrimary || !r.Status.IsWalReceiverActive || r.Status.Scheme != "http" {
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
		if inst.Status.State != "denied" || inst.Metrics.State != "denied" {
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
		if inst.Metrics.State != "unreachable" || inst.Metrics.Error != "nothing is listening on port 9187 in the Pod" {
			t.Errorf("%s metrics = %+v", inst.Pod, inst.Metrics.CNPGRuntimeSource)
		}
		if inst.Status.State != "error" || !strings.Contains(inst.Status.Error, "redirect") {
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
	useCNPGProxyAPIServer(t, func(w http.ResponseWriter, c cnpgProxyCall) {
		if c.port == "8000" {
			_, _ = io.WriteString(w, strings.Repeat("x", (1<<20)+1))
			return
		}
		cutoff := strings.Index(cnpgMetricsFixture, "# TYPE cnpg_pg_stat_database_xact_commit")
		_, _ = io.WriteString(w, cnpgMetricsFixture[:cutoff]+strings.Repeat("# filler\n", (4<<20)/9+1)+cnpgMetricsFixture[cutoff:])
	})

	_, got, body := getCNPGRuntime(t, "/api/cnpg/clusters/pgrtcap/pg-orders/runtime")
	p := got.Instances[0]
	if p.Status.State != "error" || !strings.Contains(p.Status.Error, "larger than") {
		t.Errorf("status over cap = %+v", p.Status.CNPGRuntimeSource)
	}
	if p.Metrics.State != "partial" || p.Metrics.Reason == "" || p.Metrics.XactCommitTotal != nil {
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
	cluster.SetAnnotations(map[string]string{"cnpg.io/fencedInstances": `["pg-orders-1"]`})
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

func seedCNPGPoolerChain(t *testing.T, ns string, poolerUID types.UID, conditions ...corev1.PodCondition) {
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
				Labels: map[string]string{"cnpg.io/poolerName": "pg-orders-rw"}, OwnerReferences: []metav1.OwnerReference{owner}},
			Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "pgbouncer"}}},
			Status: corev1.PodStatus{Conditions: conditions},
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
	seedCNPGPoolerChain(t, "pgrtpool", "pooler-uid", corev1.PodCondition{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable", Message: "0/2 nodes are available: insufficient cpu"})
	api := useCNPGProxyAPIServer(t, func(w http.ResponseWriter, c cnpgProxyCall) {
		if c.port != "9127" || c.path != "/metrics" || c.scheme != "http" {
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
	var got cnpgsvc.CNPGPoolerRuntimeResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Pooler.UID != "pooler-uid" || len(got.Pods) != 1 || got.Pods[0].Pod != "pg-orders-rw-abc-1" {
		t.Fatalf("pods = %s, want only the Pod on the Pooler's controller chain", body)
	}
	pod := got.Pods[0]
	if pod.SchedulingReason != "Unschedulable: 0/2 nodes are available: insufficient cpu" {
		t.Errorf("scheduling reason = %q", pod.SchedulingReason)
	}
	if pod.State != "ok" || pod.CNPGPoolerPodFacts == nil || len(pod.Pools) != 1 || !cnpgEqF(pod.Pools[0].ClWaiting, 2) {
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

func TestCNPGPoolerRuntimeProxyDeniedKeepsSchedulingEvidence(t *testing.T) {
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds,
		cnpgRuntimeWithUID(cnpgObj("postgresql.cnpg.io/v1", "Pooler", "pgrtpooldeny", "pg-orders-rw", map[string]any{"cluster": map[string]any{"name": "pg-orders"}}, nil), "pooler-denied-uid"),
	)
	seedCNPGPoolerChain(t, "pgrtpooldeny", "pooler-denied-uid", corev1.PodCondition{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable", Message: "insufficient cpu"})
	api := useCNPGProxyAPIServer(t, func(w http.ResponseWriter, c cnpgProxyCall) {
		cnpgWriteAPIStatus(w, http.StatusForbidden, metav1.StatusReasonForbidden, "pods/proxy is forbidden")
	})
	resp, err := http.Get(testServer.URL + "/api/cnpg/poolers/pgrtpooldeny/pg-orders-rw/runtime")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got cnpgsvc.CNPGPoolerRuntimeResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || got.Permission.Proxy != "denied" || len(got.Pods) != 1 {
		t.Fatalf("status %d: %+v", resp.StatusCode, got)
	}
	pod := got.Pods[0]
	if pod.State != "denied" || pod.CNPGPoolerPodFacts != nil || pod.SchedulingReason != "Unschedulable: insufficient cpu" {
		t.Fatalf("denied measurement must preserve scheduling evidence without metric facts: %+v", pod)
	}
	if len(api.seen()) != 1 {
		t.Fatalf("proxy attempts = %d, want one denied request without scheme retry", len(api.seen()))
	}
}

func TestCNPGPoolerPendingRuntime(t *testing.T) {
	for _, outcome := range []string{"unreachable", "denied", "measured"} {
		t.Run(outcome, func(t *testing.T) {
			denied := outcome == "denied"
			ns := "b7pool" + outcome
			seedCNPGWorkspace(t, cnpgWorkspaceTestKinds, cnpgRuntimeWithUID(cnpgObj("postgresql.cnpg.io/v1", "Pooler", ns, "pg-orders-rw", map[string]any{"cluster": map[string]any{"name": "pg-orders"}}, nil), ns+"-uid"))
			seedCNPGPoolerChain(t, ns, types.UID(ns+"-uid"), corev1.PodCondition{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable", Message: "2 Too many pods"})
			p, err := testFakeClient.CoreV1().Pods(ns).Get(context.Background(), "pg-orders-rw-abc-1", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			p.Status.Phase = corev1.PodPending
			if _, err = testFakeClient.CoreV1().Pods(ns).UpdateStatus(context.Background(), p, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				cached, err := k8s.GetResourceCache().Pods().Pods(ns).Get(p.Name)
				if err == nil && cached.Status.Phase == corev1.PodPending {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("pending Pod did not reach cache")
				}
				time.Sleep(20 * time.Millisecond)
			}
			useCNPGProxyAPIServer(t, func(w http.ResponseWriter, c cnpgProxyCall) {
				if denied {
					cnpgWriteAPIStatus(w, http.StatusForbidden, metav1.StatusReasonForbidden, "pods/proxy is forbidden")
				} else if outcome == "measured" {
					_, _ = w.Write([]byte(cnpgPoolerMetricsFixture))
				} else {
					http.Error(w, "address not allowed", http.StatusBadGateway)
				}
			})
			resp, err := http.Get(testServer.URL + "/api/cnpg/poolers/" + ns + "/pg-orders-rw/runtime")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var got cnpgsvc.CNPGPoolerRuntimeResponse
			if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			if len(got.Pods) != 1 {
				t.Fatalf("%+v", got)
			}
			if denied {
				if got.Permission.Proxy != "denied" || got.Pods[0].State != "denied" {
					t.Fatalf("denial must win: %+v", got)
				}
			} else if outcome == "measured" {
				if got.Pods[0].State != "ok" || got.Pods[0].CNPGPoolerPodFacts == nil {
					t.Fatalf("live read must beat stale Pod phase: %+v", got.Pods[0])
				}
			} else if got.Pods[0].Reason != "PgBouncer has not started (Pod cannot be scheduled)" || got.Pods[0].Error != "" {
				t.Fatalf("%+v", got.Pods[0])
			}
		})
	}
}
