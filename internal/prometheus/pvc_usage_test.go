package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/prom"
)

func TestPVCUsageAvailability(t *testing.T) {
	for _, tc := range []struct {
		name, used, capacity, fail, status string
		denied                             bool
	}{
		{name: "zero usage", used: "0", capacity: "1024", status: "available"},
		{name: "usage", used: "256", capacity: "1024", status: "available"},
		{name: "missing used", capacity: "1024", status: "no_series"},
		{name: "missing capacity", used: "256", status: "no_series"},
		{name: "zero capacity", used: "256", capacity: "0", status: "invalid_data"},
		{name: "negative used", used: "-1", capacity: "1024", status: "invalid_data"},
		{name: "NaN used", used: "NaN", capacity: "1024", status: "invalid_data"},
		{name: "infinite capacity", used: "256", capacity: "+Inf", status: "invalid_data"},
		{name: "integer overflow", used: "256", capacity: "1e20", status: "invalid_data"},
		{name: "used query fails", fail: "used", status: "query_failed"},
		{name: "capacity query fails", used: "256", fail: "capacity", status: "query_failed"},
		{name: "denied", denied: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				query := r.URL.Query().Get("query")
				if query == "up" {
					_, _ = w.Write([]byte(authProbeBody))
					return
				}
				calls++
				if strings.HasPrefix(query, "max(count by (") {
					_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"1"]}]}}`))
					return
				}
				value, kind := tc.capacity, "capacity"
				if strings.Contains(query, "used_bytes") {
					value, kind = tc.used, "used"
				}
				if kind == tc.fail {
					http.Error(w, "private upstream details", 500)
					return
				}
				result := "[]"
				if value != "" {
					result = fmt.Sprintf(`[{"metric":{},"value":[1700000000,%q]}]`, value)
				}
				_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":%s}}`, result)
			}))
			defer upstream.Close()
			Initialize(nil, nil, "pvc-test")
			SetManualURL(upstream.URL)
			SetAuthGate(func(_ *http.Request, group, resource, namespace, verb string) bool {
				if group != "" || resource != "persistentvolumeclaims" || namespace != "demo" || verb != "get" {
					t.Errorf("unexpected authorization: %s/%s %s %s", group, resource, namespace, verb)
				}
				return !tc.denied
			})
			defer func() { SetAuthGate(nil); Reset(); Initialize(nil, nil, "") }()
			w := httptest.NewRecorder()
			metricsRouter().ServeHTTP(w, httptest.NewRequest("GET", "/prometheus/pvc/demo/disk", nil))
			if tc.denied {
				if w.Code != 403 || calls != 0 {
					t.Fatalf("denied: status=%d queries=%d", w.Code, calls)
				}
				return
			}
			if w.Code != 200 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			var response PVCUsageResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Status != tc.status || response.HasData != (tc.status == "available") {
				t.Fatalf("response=%+v", response)
			}
			if response.HasData && (response.Capacity != 1024 || (tc.used == "0" && (response.Used != 0 || response.Ratio != 0))) {
				t.Fatalf("wrong measurement: %+v", response)
			}
			if !response.HasData && (response.Used != 0 || response.Capacity != 0 || response.Ratio != 0) {
				t.Fatalf("unavailable response has measurements: %+v", response)
			}
			if strings.Contains(w.Body.String(), "private upstream") {
				t.Fatal("raw query error exposed")
			}
		})
	}
}

// Same-named claims in two clusters sharing one Prometheus: east is 95 of
// 100 GiB, west 10 of 1000 GiB. Unscoped max() would report 95 of 1000.
func TestQueryPVCUsageHoldsToOneClusterIdentity(t *testing.T) {
	const gi = 1 << 30
	q := &fakeScanQuerier{queryFn: func(query string) (*prom.QueryResult, error) {
		east := strings.Contains(query, `cluster="east"`)
		switch {
		case strings.HasPrefix(query, "max(count by ("):
			return &prom.QueryResult{Series: []prom.Series{vec(nil, 2)}}, nil
		case strings.Contains(query, "used_bytes") && east:
			return &prom.QueryResult{Series: []prom.Series{vec(map[string]string{"persistentvolumeclaim": "pg-1"}, 95*gi)}}, nil
		case strings.Contains(query, "capacity_bytes") && east:
			return &prom.QueryResult{Series: []prom.Series{vec(map[string]string{"persistentvolumeclaim": "pg-1"}, 100*gi)}}, nil
		case strings.Contains(query, "used_bytes"):
			return &prom.QueryResult{Series: []prom.Series{vec(map[string]string{"persistentvolumeclaim": "pg-1"}, 95*gi)}}, nil
		default:
			return &prom.QueryResult{Series: []prom.Series{vec(map[string]string{"persistentvolumeclaim": "pg-1"}, 1000*gi)}}, nil
		}
	}}
	_, _, err := decideScope(context.Background(), q, pvcScopeProbe("db", []string{"pg-1"}, 0), nil)
	if out, failed := pvcScopeFailure(err); !failed || out.Status != PVCUsageAmbiguous || len(out.Usage) != 0 {
		t.Fatalf("two identities, unproven = %+v (err %v), want ambiguous with no value", out, err)
	}

	m, _, err := decideScope(context.Background(), q, pvcScopeProbe("db", []string{"pg-1"}, 0), map[string]string{"cluster": "east"})
	if err != nil {
		t.Fatal(err)
	}
	out := queryPVCUsage(context.Background(), q, "db", []string{"pg-1"}, m)
	if u := out.Usage["pg-1"]; out.Status != PVCUsageAvailable || u.CapacityBytes != 100*gi || u.Ratio < 0.94 {
		t.Fatalf("proven east = %+v, want 95 of 100 GiB", out)
	}
}

func TestPVCUsageHandlerRefusesMergedClusters(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		if query == "up" {
			_, _ = w.Write([]byte(authProbeBody))
			return
		}
		result := `[{"metric":{},"value":[1700000000,"1024"]}]`
		if strings.HasPrefix(query, "max(count by (") {
			result = `[{"metric":{},"value":[1700000000,"2"]}]`
		}
		_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":%s}}`, result)
	}))
	defer upstream.Close()
	Initialize(nil, nil, "pvc-test")
	SetManualURL(upstream.URL)
	SetAuthGate(func(*http.Request, string, string, string, string) bool { return true })
	defer func() { SetAuthGate(nil); Reset(); Initialize(nil, nil, "") }()
	w := httptest.NewRecorder()
	metricsRouter().ServeHTTP(w, httptest.NewRequest("GET", "/prometheus/pvc/demo/disk", nil))
	var response PVCUsageResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != PVCUsageAmbiguous || response.HasData || response.Used != 0 {
		t.Fatalf("response = %+v, want ambiguous_scope without a value", response)
	}
}

func TestPVCUsageSingleClusterScrapes(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		labelledIdentities, unlabelled int
		status                         string
	}{
		{"unlabelled only", 0, 1, PVCUsageAvailable},
		{"one identity", 1, 0, PVCUsageAvailable},
		{"one identity and unlabelled scrape", 1, 1, PVCUsageAvailable},
		{"two identities", 2, 0, PVCUsageAmbiguous},
		{"two identities and unlabelled scrape", 2, 1, PVCUsageAmbiguous},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				query := r.URL.Query().Get("query")
				if query == "up" {
					_, _ = w.Write([]byte(authProbeBody))
					return
				}
				value := 1024
				if strings.HasPrefix(query, "max(count by (") {
					value = tc.labelledIdentities + tc.unlabelled
					if strings.Contains(query, "unless on (cluster,cluster_name,k8s_cluster_name,kubernetes_cluster,cluster_id) vector(1)") {
						value = tc.labelledIdentities
					}
				} else if strings.Contains(query, "used_bytes") {
					value = 256
				}
				_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"%d"]}]}}`, value)
			}))
			defer upstream.Close()
			Initialize(nil, nil, "pvc-test")
			SetManualURL(upstream.URL)
			SetAuthGate(func(*http.Request, string, string, string, string) bool { return true })
			defer func() { SetAuthGate(nil); Reset(); Initialize(nil, nil, "") }()
			w := httptest.NewRecorder()
			metricsRouter().ServeHTTP(w, httptest.NewRequest("GET", "/prometheus/pvc/demo/disk", nil))
			var response PVCUsageResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Status != tc.status {
				t.Fatalf("response=%+v, want %s", response, tc.status)
			}
			if tc.status == PVCUsageAvailable && (!response.HasData || response.Used != 256 || response.Capacity != 1024 || response.Ratio != .25) {
				t.Fatalf("single-cluster value lost: %+v", response)
			}
			if tc.status == PVCUsageAmbiguous && (response.HasData || response.Used != 0 || response.Capacity != 0) {
				t.Fatalf("ambiguous value exposed: %+v", response)
			}
		})
	}
}

func TestPVCUsageHandlerKeepsCapturedClient(t *testing.T) {
	replacementCalls := 0
	replacement := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		replacementCalls++
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"2"]}]}}`))
	}))
	defer replacement.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		if query == "up" {
			_, _ = w.Write([]byte(authProbeBody))
			return
		}
		value := 1024
		if strings.HasPrefix(query, "max(count by (") {
			value = 1
		} else if strings.Contains(query, "used_bytes") {
			value = 256
		}
		_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"%d"]}]}}`, value)
	}))
	defer upstream.Close()
	Initialize(nil, nil, "original")
	SetManualURL(upstream.URL)
	SetAuthGate(func(*http.Request, string, string, string, string) bool {
		Initialize(nil, nil, "replacement")
		SetManualURL(replacement.URL)
		return true
	})
	defer func() { SetAuthGate(nil); Reset(); Initialize(nil, nil, "") }()
	w := httptest.NewRecorder()
	metricsRouter().ServeHTTP(w, httptest.NewRequest("GET", "/prometheus/pvc/demo/disk", nil))
	var response PVCUsageResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != PVCUsageAvailable || response.Used != 256 || response.Capacity != 1024 || replacementCalls != 0 {
		t.Fatalf("handler changed clients: response=%+v replacement queries=%d", response, replacementCalls)
	}
}

func vec(labels map[string]string, v float64) prom.Series {
	return prom.Series{Labels: labels, DataPoints: []prom.DataPoint{{Timestamp: 1, Value: v}}}
}

func TestPVCUsageUnlabeledProofExcludesForeignSeries(t *testing.T) {
	for _, localPresent := range []bool{true, false} {
		t.Run(fmt.Sprintf("local present=%t", localPresent), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				query := r.Form.Get("query")
				if query == "up" {
					_, _ = w.Write([]byte(authProbeBody))
					return
				}
				if strings.Contains(query, "kube_pod_info") {
					_, _ = w.Write(attributionEvidence([]prom.Series{{Labels: map[string]string{"namespace": "db", "pod": "pg-0", "uid": "local-uid"}}}, ""))
					return
				}
				unlabeled := true
				for _, label := range partitionLabels {
					unlabeled = unlabeled && strings.Contains(query, label+`=""`)
				}
				var rows []prom.Series
				value := float64(1000)
				if unlabeled {
					value = 100
					if strings.Contains(query, "used_bytes") {
						value = 95
					}
				}
				if !unlabeled || localPresent {
					rows = []prom.Series{vec(map[string]string{"persistentvolumeclaim": "pg-data"}, value)}
				}
				result := make([]map[string]any, 0, len(rows))
				for _, row := range rows {
					result = append(result, map[string]any{"metric": row.Labels, "value": []any{100, fmt.Sprintf("%g", value)}})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": result}})
			}))
			defer upstream.Close()
			c := &Client{baseURL: upstream.URL, httpClient: upstream.Client()}
			out := c.QueryPVCUsage(context.Background(), "db", []string{"pg-data"}, []prom.WorkloadPodIdentity{{Name: "pg-0", UID: "local-uid"}})
			if localPresent {
				usage := out.Usage["pg-data"]
				if out.Status != PVCUsageAvailable || usage.UsedBytes != 95 || usage.CapacityBytes != 100 || usage.Ratio != 0.95 || out.Isolation.Mode != SeriesIsolationVerified {
					t.Fatalf("foreign stats merged into local claim: %+v", out)
				}
			} else if out.Status != PVCUsageScopeMismatch || len(out.Usage) != 0 {
				t.Fatalf("foreign-only stats used for a local claim: %+v", out)
			}
		})
	}
}
