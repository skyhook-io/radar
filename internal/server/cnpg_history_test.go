package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	cnpgsvc "github.com/skyhook-io/radar/internal/cnpg"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
)

// useCNPGHistoryPrometheus serves a Prometheus whose range queries answer
// replay lag for pg-orders-2 and whose instant queries report pg-orders-1
// scraped as primary and pg-orders-2 as a standby 4 s behind.
func useCNPGHistoryPrometheus(t *testing.T) *[]string {
	t.Helper()
	return useCNPGHistoryPrometheusAnswering(t, nil)
}

// useCNPGHistoryPrometheusAnswering is useCNPGHistoryPrometheus with answer
// consulted first for instant queries: it returns the result array's JSON, or
// "" to fall through.
func useCNPGHistoryPrometheusAnswering(t *testing.T, answer func(q string) string) *[]string {
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
		if answer != nil {
			if result := answer(q); result != "" {
				_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"vector","result":`+result+`}}`)
				return
			}
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

func getCNPGHistory(t *testing.T, path string) (int, cnpgsvc.CNPGClusterHistoryResponse, string) {
	t.Helper()
	resp, err := http.Get(testServer.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var got cnpgsvc.CNPGClusterHistoryResponse
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
	if got.Source != "none" || got.Reason == "" || len(got.Charts) != 0 {
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
	if got.Source != "prometheus" || got.State != "ok" || got.StepSeconds != 30 || got.Isolation == nil || got.Isolation.Mode != prometheuspkg.SeriesIsolationUnverified {
		t.Fatalf("got %+v", got)
	}
	// The volume chart's claims are matched apart from the instance Pods, and say so.
	if got.PVCIsolation == nil || got.PVCIsolation.Mode != prometheuspkg.SeriesIsolationUnverified || !strings.Contains(got.PVCIsolation.Note, "claim names") {
		t.Errorf("pvcIsolation = %+v", got.PVCIsolation)
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
	perms.SetCanI("get", cnpgsvc.Group, "clusters", "pghi3", true)
	allow(perms, "", "persistentvolumeclaims", "pghi3", false)
	allow(perms, "", "pods", "pghi3", false)
	env.srv.permCache.Set("dba", nil, perms)

	resp := env.authGet(t, "/api/cnpg/clusters/pghi3/pg-orders/history?range=15m", "dba", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, b)
	}
	var got cnpgsvc.CNPGClusterHistoryResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	for _, c := range got.Charts {
		if c.State != prometheuspkg.CNPGHistoryStateDenied || len(c.Series) != 0 || c.Grant == nil {
			t.Errorf("%s = %+v, want denied without series", c.ID, c)
		}
	}

	perms2 := &auth.UserPermissions{AllowedNamespaces: []string{"pghi3"}}
	perms2.SetCanI("get", cnpgsvc.Group, "clusters", "pghi3", false)
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
	var got cnpgsvc.CNPGFleetMetricsResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Source != "prometheus" || len(got.Clusters) != 1 {
		t.Fatalf("got %+v", got)
	}
	c := got.Clusters[0]
	if c.Lag.State != "ok" || c.Lag.Seconds == nil || *c.Lag.Seconds != 4 || c.Lag.Pod != "pg-orders-2" {
		t.Errorf("lag = %+v", c.Lag)
	}
	if c.Growth.State != "noSeries" || c.Growth.BytesPerHour != nil {
		t.Errorf("growth = %+v", c.Growth)
	}
	if !c.Lag.ReceiverUnknown || c.Lag.Receiving != nil || c.Lag.ReceiverDown != nil {
		t.Errorf("no receiver series must read unknown, never receiving: %+v", c.Lag)
	}
	if c.Slots.State != "noSeries" || c.Slots.Inactive != nil {
		t.Errorf("slots = %+v, want noSeries without a list", c.Slots)
	}
}

// pg-orders-2 receives nothing: its lag reads 0 because nothing is left to
// replay, while the primary keeps its slot inactive with 4.9 GB of WAL.
func TestCNPGFleetMetricsReceiverDownAndInactiveSlot(t *testing.T) {
	seedCNPGStorageCluster(t, "pgfr")
	row := func(labels string, v string) string {
		return `{"metric":{` + labels + `},"value":[1700000000,"` + v + `"]}`
	}
	seen := useCNPGHistoryPrometheusAnswering(t, func(q string) string {
		switch {
		case strings.HasPrefix(q, "(max by (pod) (cnpg_pg_replication_lag"):
			return "[" + row(`"pod":"pg-orders-1"`, "-1") + "," + row(`"pod":"pg-orders-2"`, "0") + "]"
		case strings.HasPrefix(q, "(max by (pod) (cnpg_pg_replication_is_wal_receiver_up"):
			return "[" + row(`"pod":"pg-orders-2"`, "0") + "]"
		case strings.HasPrefix(q, "label_replace("):
			return "[" + strings.Join([]string{
				row(`"pod":"pg-orders-1","slot_name":"_cnpg_pg_orders_2","radar_row":"inactive"`, "0"),
				row(`"pod":"pg-orders-1","slot_name":"_cnpg_pg_orders_2","radar_row":"retained"`, "4900000000"),
				row(`"pod":"pg-orders-1","radar_row":"reported"`, "1"),
				row(`"pod":"pg-orders-1","radar_row":"recovery"`, "0"),
				row(`"pod":"pg-orders-2","radar_row":"recovery"`, "1"),
				row(`"pod":"pg-orders-1","radar_row":"scraped"`, "1"),
				row(`"pod":"pg-orders-2","radar_row":"scraped"`, "1"),
			}, ",") + "]"
		}
		return ""
	})
	resp, err := http.Get(testServer.URL + "/api/cnpg/fleet-metrics?namespaces=pgfr")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var raw struct {
		ReceiverSource string           `json:"receiverSource"`
		SlotsSource    string           `json:"slotsSource"`
		Clusters       []map[string]any `json:"clusters"`
	}
	if err := json.Unmarshal(body, &raw); err != nil || len(raw.Clusters) != 1 {
		t.Fatalf("decode %v: %s", err, body)
	}
	if raw.ReceiverSource == "" || raw.SlotsSource == "" {
		t.Errorf("sources missing: %s", body)
	}
	lag := raw.Clusters[0]["lag"].(map[string]any)
	for k, want := range map[string]any{"state": "ok", "seconds": 0.0, "pod": "pg-orders-2", "standbys": 1.0, "receiving": 0.0} {
		if lag[k] != want {
			t.Errorf("lag.%s = %v, want %v (%v)", k, lag[k], want, lag)
		}
	}
	if down, _ := lag["receiverDown"].([]any); len(down) != 1 || down[0] != "pg-orders-2" {
		t.Errorf("lag.receiverDown = %v", lag["receiverDown"])
	}
	if _, ok := lag["receiverUnknown"]; ok {
		t.Errorf("receiverUnknown set although the receiver was read: %v", lag)
	}
	slots := raw.Clusters[0]["slots"].(map[string]any)
	inactive, _ := slots["inactive"].([]any)
	if slots["state"] != "ok" || len(inactive) != 1 || slots["isolation"] == nil {
		t.Fatalf("slots = %v", slots)
	}
	want := map[string]any{"slot": "_cnpg_pg_orders_2", "pod": "pg-orders-1", "role": "primary", "bytes": 4.9e9}
	got := inactive[0].(map[string]any)
	if len(got) != len(want) {
		t.Errorf("slot fields = %v, want exactly %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("slot.%s = %v, want %v", k, got[k], v)
		}
	}
	for _, q := range *seen {
		if (strings.Contains(q, "is_wal_receiver_up") || strings.Contains(q, "cnpg_pg_replication_slots")) && !strings.Contains(q, `namespace="pgfr",pod=~"^pg-orders-[0-9]+$"`) {
			t.Errorf("query not scoped to the cluster's instances: %s", q)
		}
	}
}

func TestCNPGFleetMetricsSlotsFollowPodsGate(t *testing.T) {
	seedCNPGStorageCluster(t, "pgfd")
	seen := useCNPGHistoryPrometheus(t)
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"pgfd"}}
	allow(perms, cnpgsvc.Group, "clusters", "", true)
	allow(perms, cnpgsvc.Group, "clusters", "pgfd", true)
	perms.SetCanI("get", "", "pods", "pgfd", false)
	allow(perms, "", "persistentvolumeclaims", "pgfd", false)
	env.srv.permCache.Set("dba", nil, perms)

	resp := env.authGet(t, "/api/cnpg/fleet-metrics?namespaces=pgfd", "dba", "")
	defer resp.Body.Close()
	var got cnpgsvc.CNPGFleetMetricsResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Clusters) != 1 {
		t.Fatalf("got %+v", got)
	}
	c := got.Clusters[0]
	for name, st := range map[string]struct {
		State string
		Grant *Grant
	}{"lag": {c.Lag.State, c.Lag.Grant}, "slots": {c.Slots.State, c.Slots.Grant}} {
		if st.State != "denied" || st.Grant == nil || st.Grant.Verb != "get" || st.Grant.Resource != "pods" || st.Grant.Namespace != "pgfd" {
			t.Errorf("%s = %s %+v, want denied naming get pods in pgfd", name, st.State, st.Grant)
		}
	}
	if c.Slots.Inactive != nil || c.Lag.Standbys != nil {
		t.Errorf("denied reads still carry data: %+v", c)
	}
	for _, q := range *seen {
		if strings.Contains(q, "cnpg_") {
			t.Errorf("denied caller's CNPG series were queried: %s", q)
		}
	}
}
