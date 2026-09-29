package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
)

// useCNPGHistoryPrometheus serves a Prometheus whose range queries answer
// replay lag for pg-orders-2 and whose instant queries report pg-orders-1
// scraped as primary and pg-orders-2 as a standby 4 s behind.
func useCNPGHistoryPrometheus(t *testing.T) *[]string {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = r.ParseForm()
		q := r.Form.Get("query")
		mu.Lock()
		seen = append(seen, q)
		mu.Unlock()
		empty := func(kind string) string {
			return `{"status":"success","data":{"resultType":"` + kind + `","result":[]}}`
		}
		if strings.HasSuffix(r.URL.Path, "/query_range") {
			if strings.Contains(q, "cnpg_pg_replication_lag") {
				_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"pod":"pg-orders-2","job":"x"},"values":[[1700000000,"1"],[1700000030,"4"]]}]}}`)
				return
			}
			_, _ = io.WriteString(w, empty("matrix"))
			return
		}
		switch {
		case q == "up":
			_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"job":"prometheus"},"value":[1700000000,"1"]}]}}`)
		case strings.HasPrefix(q, "(max by (pod) (cnpg_pg_replication_lag"):
			_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"pod":"pg-orders-1"},"value":[1700000000,"-1"]},{"metric":{"pod":"pg-orders-2"},"value":[1700000000,"4"]}]}}`)
		case strings.HasPrefix(q, "max(count by (pod)"):
			_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"1"]}]}}`)
		default:
			_, _ = io.WriteString(w, empty("vector"))
		}
	}))
	prometheuspkg.Initialize(nil, nil, "test")
	prometheuspkg.SetManualURL(srv.URL)
	t.Cleanup(func() {
		srv.Close()
		prometheuspkg.Reset()
		prometheuspkg.Initialize(nil, nil, "")
	})
	return &seen
}

func getCNPGHistory(t *testing.T, path string) (int, CNPGClusterHistoryResponse, string) {
	t.Helper()
	resp, err := http.Get(testServer.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var got CNPGClusterHistoryResponse
	_ = json.Unmarshal(body, &got)
	return resp.StatusCode, got, string(body)
}

func TestCNPGClusterHistory_NoPrometheusSaysSo(t *testing.T) {
	seedCNPGStorageCluster(t, "pghi1")
	prometheuspkg.Reset()
	status, got, body := getCNPGHistory(t, "/api/cnpg/clusters/pghi1/pg-orders/history?range=15m")
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	if got.Source != cnpgHistorySourceNone || got.Reason == "" || len(got.Charts) != 0 {
		t.Fatalf("got %+v", got)
	}
	if status, _, _ := getCNPGHistory(t, "/api/cnpg/clusters/pghi1/pg-orders/history?range=7d"); status != http.StatusBadRequest {
		t.Errorf("range 7d: %d, want 400", status)
	}
	if status, _, _ := getCNPGHistory(t, "/api/cnpg/clusters/pghi1/nope/history"); status != http.StatusNotFound {
		t.Errorf("unknown cluster: %d, want 404", status)
	}
}

func TestCNPGClusterHistory_ChartsFromPrometheus(t *testing.T) {
	seedCNPGStorageCluster(t, "pghi2")
	seen := useCNPGHistoryPrometheus(t)
	status, got, body := getCNPGHistory(t, "/api/cnpg/clusters/pghi2/pg-orders/history?range=1h")
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	if got.Source != cnpgHistorySourcePrometheus || got.State != cnpgHistoryStateOK || got.StepSeconds != 30 || got.Isolation == nil || got.Isolation.Mode != prometheuspkg.CNPGIsolationUnverified {
		t.Fatalf("got %+v", got)
	}
	by := map[string]prometheuspkg.CNPGHistoryChart{}
	for _, c := range got.Charts {
		by[c.ID] = c
	}
	if lag := by["replicationLag"]; lag.State != prometheuspkg.CNPGHistoryStateOK || len(lag.Series) != 1 || lag.Series[0].Labels["pod"] != "pg-orders-2" {
		t.Errorf("lag = %+v", lag)
	}
	if pvc := by["pvcUsed"]; pvc.State != prometheuspkg.CNPGHistoryStateNoSeries || !strings.Contains(pvc.Reason, "not scraped") {
		t.Errorf("pvc = %+v", pvc)
	}
	var pvcQuery string
	for _, q := range *seen {
		if strings.Contains(q, "kubelet_volume_stats_used_bytes") {
			pvcQuery = q
		}
	}
	if !strings.Contains(pvcQuery, "pg-orders-1-wal") || strings.Contains(pvcQuery, "pg-orders-9") {
		t.Errorf("PVC query must name owned claims only: %s", pvcQuery)
	}
	if !strings.Contains(body, `"value":null`) && strings.Contains(body, `"value":0,`) {
		t.Errorf("a gap was rendered as zero: %s", body)
	}
}

func TestCNPGClusterHistory_GatesPerSource(t *testing.T) {
	seedCNPGStorageCluster(t, "pghi3")
	useCNPGHistoryPrometheus(t)
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"pghi3"}}
	perms.SetCanI("get", cnpgGroup, "clusters", "pghi3", true)
	allow(perms, "", "persistentvolumeclaims", "pghi3", false)
	allow(perms, "", "pods", "pghi3", false)
	env.srv.permCache.Set("dba", nil, perms)

	resp := env.authGet(t, "/api/cnpg/clusters/pghi3/pg-orders/history?range=15m", "dba", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, b)
	}
	var got CNPGClusterHistoryResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	for _, c := range got.Charts {
		if c.State != prometheuspkg.CNPGHistoryStateDenied || len(c.Series) != 0 || c.Grant == "" {
			t.Errorf("%s = %+v, want denied without series", c.ID, c)
		}
	}

	perms2 := &auth.UserPermissions{AllowedNamespaces: []string{"pghi3"}}
	perms2.SetCanI("get", cnpgGroup, "clusters", "pghi3", false)
	env.srv.permCache.Set("nobody", nil, perms2)
	denied := env.authGet(t, "/api/cnpg/clusters/pghi3/pg-orders/history", "nobody", "")
	denied.Body.Close()
	if denied.StatusCode != http.StatusForbidden {
		t.Errorf("without get clusters: %d, want 403", denied.StatusCode)
	}
}

func TestCNPGFleetMetrics(t *testing.T) {
	seedCNPGStorageCluster(t, "pgfm")
	useCNPGHistoryPrometheus(t)
	resp, err := http.Get(testServer.URL + "/api/cnpg/fleet-metrics?namespaces=pgfm")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got CNPGFleetMetricsResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Source != cnpgHistorySourcePrometheus || len(got.Clusters) != 1 {
		t.Fatalf("got %+v", got)
	}
	c := got.Clusters[0]
	if c.Lag.State != cnpgHistoryStateOK || c.Lag.Seconds == nil || *c.Lag.Seconds != 4 || c.Lag.Pod != "pg-orders-2" {
		t.Errorf("lag = %+v", c.Lag)
	}
	if c.Growth.State != cnpgUsageStateNoSeries || c.Growth.BytesPerHour != nil {
		t.Errorf("growth = %+v", c.Growth)
	}

}

func TestParseCNPGLogQueryInterval(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	q, err := parseCNPGLogQuery(httptest.NewRequest("GET", "/?sinceTime=2026-09-28T11:00:00Z&untilTime=2026-09-28T11:05:00Z", nil), now)
	if err != nil || q.tailLines != 0 || q.sinceSeconds == nil || *q.sinceSeconds != 3600 {
		t.Fatalf("q = %+v err = %v", q, err)
	}
	if !q.keep(workloadLogEntry{Timestamp: "2026-09-28T11:05:00Z"}) || q.keep(workloadLogEntry{Timestamp: "2026-09-28T11:05:00.1Z"}) || q.keep(workloadLogEntry{Timestamp: "2026-09-28T10:59:59Z"}) {
		t.Fatal("interval bounds not applied")
	}
	for _, bad := range []string{"/?untilTime=2026-09-28T11:05:00Z", "/?sinceTime=2026-09-28T11:05:00Z&untilTime=2026-09-28T11:00:00Z", "/?sinceTime=2026-09-28T11:00:00Z&untilTime=x"} {
		if _, err := parseCNPGLogQuery(httptest.NewRequest("GET", bad, nil), now); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}
