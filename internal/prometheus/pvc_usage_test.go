package prometheus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/prom"
	"github.com/skyhook-io/radar/pkg/prom/promtest"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
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
				if strings.HasPrefix(query, "topk(2,") {
					_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"cluster":"single"},"value":[1700000000,"1"]}]}}`))
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
					result = fmt.Sprintf(`[{"metric":{"persistentvolumeclaim":"disk"},"value":[1700000000,%q]}]`, value)
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
		case strings.HasPrefix(query, "topk(2,"):
			rows := []prom.Series{vec(map[string]string{"cluster": "east"}, 1)}
			if !east {
				rows = append(rows, vec(map[string]string{"cluster": "west"}, 1))
			}
			return &prom.QueryResult{Series: rows}, nil
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
		result := `[{"metric":{"persistentvolumeclaim":"disk"},"value":[1700000000,"1024"]}]`
		if strings.HasPrefix(query, "topk(2,") {
			result = `[{"metric":{"cluster":"east"},"value":[1700000000,"1"]},{"metric":{"cluster":"west"},"value":[1700000000,"1"]}]`
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
				if strings.HasPrefix(query, "topk(2,") {
					rows := []prom.Series{}
					for i := 0; i < tc.labelledIdentities; i++ {
						rows = append(rows, vec(map[string]string{"cluster": fmt.Sprint(i)}, 1))
					}
					if !strings.Contains(query, "unless on (") && tc.unlabelled > 0 {
						rows = append(rows, vec(nil, 1))
					}
					_, _ = w.Write(attributionEvidence(rows, ""))
					return
				}
				value := 1024
				if strings.Contains(query, "used_bytes") {
					value = 256
				}
				_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"persistentvolumeclaim":"disk"},"value":[1700000000,"%d"]}]}}`, value)
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
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"persistentvolumeclaim":"disk"},"value":[1700000000,"2"]}]}}`))
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
		_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"persistentvolumeclaim":"disk"},"value":[1700000000,"%d"]}]}}`, value)
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
				if out.Status != PVCUsageAvailable || usage.UsedBytes != 95 || usage.CapacityBytes != 100 || usage.Ratio != 0.95 || out.Isolation.Mode != SeriesIsolationUnverified || !strings.Contains(out.Isolation.Note, "unlabeled stats from another cluster") {
					t.Fatalf("foreign stats merged into local claim: %+v", out)
				}
			} else if out.Status != PVCUsageScopeMismatch || len(out.Usage) != 0 {
				t.Fatalf("foreign-only stats used for a local claim: %+v", out)
			}
		})
	}
}

func TestPVCUsageRefusesSplitMetricPartitions(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		query := r.Form.Get("query")
		if query == "up" {
			_, _ = w.Write([]byte(authProbeBody))
			return
		}
		if strings.HasPrefix(query, "topk(2,") {
			_, _ = w.Write(attributionEvidence([]prom.Series{vec(map[string]string{"cluster": "east"}, 1), vec(map[string]string{"cluster": "west"}, 1)}, ""))
			return
		}
		value := "100"
		if strings.Contains(query, "used_bytes") {
			value = "95"
		}
		_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"persistentvolumeclaim":"data"},"value":[100,%q]}]}}`, value)
	}))
	defer upstream.Close()
	c := &Client{baseURL: upstream.URL, httpClient: upstream.Client()}
	out := c.QueryPVCUsage(context.Background(), "db", []string{"data"}, nil)
	if out.Status != PVCUsageAmbiguous || len(out.Usage) != 0 {
		t.Fatalf("used from west paired with capacity from east: %+v", out)
	}
	for _, window := range []time.Duration{0, time.Hour} {
		if _, _, err := c.ResolvePVCScope(context.Background(), "db", []string{"data"}, nil, window); !errors.Is(err, ErrScopeAmbiguous) {
			t.Fatalf("split metric scope accepted over %s: %v", window, err)
		}
	}
}

func TestPVCScopePromQL(t *testing.T) {
	image := os.Getenv("RADAR_TEST_PROMTOOL_IMAGE")
	if image == "" {
		t.Skip("set RADAR_TEST_PROMTOOL_IMAGE=prom/prometheus:v3.5.0 to evaluate queries with Docker")
	}
	input := []map[string]string{}
	tests := []map[string]any{}
	for _, tc := range []struct {
		claim            string
		split, duplicate bool
		identities       int
	}{
		{claim: "same", identities: 1},
		{claim: "split", split: true, identities: 2},
		{claim: "duplicate", duplicate: true, identities: 1},
	} {
		for _, metric := range []string{"kubelet_volume_stats_capacity_bytes", "kubelet_volume_stats_used_bytes"} {
			cluster := "east"
			if tc.split && metric == "kubelet_volume_stats_used_bytes" {
				cluster = "west"
			}
			input = append(input, map[string]string{
				"series": fmt.Sprintf(`%s{namespace="db",persistentvolumeclaim=%q,cluster=%q}`, metric, tc.claim, cluster),
				"values": "100+0x10",
			})
			if tc.duplicate {
				input = append(input, map[string]string{
					"series": fmt.Sprintf(`%s{namespace="db",persistentvolumeclaim=%q}`, metric, tc.claim),
					"values": "100+0x10",
				})
			}
		}
		for _, window := range []time.Duration{0, time.Hour} {
			probe := pvcScopeProbe("db", []string{tc.claim}, window)
			q := &fakeScanQuerier{queryFn: func(query string) (*prom.QueryResult, error) {
				samples := []map[string]any{{"labels": "{}", "value": tc.identities}}
				if strings.HasPrefix(query, "topk(2,") {
					samples = []map[string]any{{"labels": `{cluster="east"}`, "value": 1}}
					if tc.split {
						samples = append(samples, map[string]any{"labels": `{cluster="west"}`, "value": 1})
					}
				}
				tests = append(tests, map[string]any{"expr": query, "eval_time": "10m", "exp_samples": samples})
				return &prom.QueryResult{}, nil
			}}
			if _, _, err := decideScope(context.Background(), q, probe, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, tc := range []struct {
		claim, cluster string
		missing        bool
	}{
		{claim: "verified", cluster: "east"},
		{claim: "unlabeled"},
		{claim: "foreign-only", missing: true},
	} {
		if !tc.missing {
			for metric, value := range map[string]int{"kubelet_volume_stats_used_bytes": 10, "kubelet_volume_stats_capacity_bytes": 100} {
				input = append(input, map[string]string{
					"series": fmt.Sprintf(`%s{namespace="db",persistentvolumeclaim=%q,cluster=%q}`, metric, tc.claim, tc.cluster),
					"values": fmt.Sprintf("%d+0x10", value),
				})
			}
		}
		for metric, value := range map[string]int{"kubelet_volume_stats_used_bytes": 95, "kubelet_volume_stats_capacity_bytes": 1000} {
			input = append(input, map[string]string{
				"series": fmt.Sprintf(`%s{namespace="db",persistentvolumeclaim=%q,cluster="west"}`, metric, tc.claim),
				"values": fmt.Sprintf("%d+0x10", value),
			})
		}
		matchers := `cluster="east"`
		if tc.cluster == "" {
			parts := make([]string, len(partitionLabels))
			for i, label := range partitionLabels {
				parts[i] = label + `=""`
			}
			matchers = strings.Join(parts, ",")
		}
		for _, window := range []time.Duration{0, time.Hour} {
			q := &fakeScanQuerier{queryFn: func(query string) (*prom.QueryResult, error) {
				result := &prom.QueryResult{}
				samples := []map[string]any{}
				if strings.HasPrefix(query, "topk(2,") {
					if !tc.missing {
						labels, expected := map[string]string{}, "{}"
						if tc.cluster != "" {
							labels["cluster"], expected = tc.cluster, fmt.Sprintf(`{cluster=%q}`, tc.cluster)
						}
						result.Series = []prom.Series{vec(labels, 1)}
						samples = append(samples, map[string]any{"labels": expected, "value": 1})
					}
				} else if !tc.missing || !strings.Contains(query, matchers) {
					result.Series = []prom.Series{vec(nil, 1)}
					samples = append(samples, map[string]any{"labels": "{}", "value": 1})
				}
				tests = append(tests, map[string]any{"expr": query, "eval_time": "10m", "exp_samples": samples})
				return result, nil
			}}
			_, _, err := decideVerifiedScope(context.Background(), q, pvcScopeProbe("db", []string{tc.claim}, window), matchers, SeriesIsolation{})
			if tc.missing && !errors.Is(err, ErrScopeMismatch) || !tc.missing && err != nil {
				t.Fatalf("scope for %s over %s: %v", tc.claim, window, err)
			}
		}
		q := &fakeScanQuerier{queryFn: func(query string) (*prom.QueryResult, error) {
			samples := []map[string]any{}
			if !tc.missing {
				value := 100
				if strings.Contains(query, "used_bytes") {
					value = 10
				}
				samples = append(samples, map[string]any{"labels": fmt.Sprintf(`{persistentvolumeclaim=%q}`, tc.claim), "value": value})
			}
			tests = append(tests, map[string]any{"expr": query, "eval_time": "10m", "exp_samples": samples})
			return &prom.QueryResult{}, nil
		}}
		queryPVCUsage(context.Background(), q, "db", []string{tc.claim}, matchers)
	}
	for _, clusterID := range []string{"east", "west"} {
		for _, metric := range []string{"kubelet_volume_stats_capacity_bytes", "kubelet_volume_stats_used_bytes"} {
			input = append(input, map[string]string{"series": fmt.Sprintf(`%s{namespace="db",persistentvolumeclaim="collision",cluster="shared",cluster_id=%q}`, metric, clusterID), "values": "100+0x10"})
		}
	}
	for _, window := range []time.Duration{0, time.Hour} {
		q := &fakeScanQuerier{queryFn: func(query string) (*prom.QueryResult, error) {
			samples := []map[string]any{{"labels": "{}", "value": 2}}
			rows := []prom.Series{vec(nil, 2)}
			if strings.HasPrefix(query, "topk(2,") {
				samples = []map[string]any{{"labels": `{cluster="shared",cluster_id="east"}`, "value": 1}, {"labels": `{cluster="shared",cluster_id="west"}`, "value": 1}}
				rows = []prom.Series{vec(map[string]string{"cluster": "shared", "cluster_id": "east"}, 1), vec(map[string]string{"cluster": "shared", "cluster_id": "west"}, 1)}
			}
			tests = append(tests, map[string]any{"expr": query, "eval_time": "10m", "exp_samples": samples})
			return &prom.QueryResult{Series: rows}, nil
		}}
		if _, _, err := decideScope(context.Background(), q, pvcScopeProbe("db", []string{"collision"}, window), map[string]string{"cluster": "shared"}); !errors.Is(err, ErrScopeAmbiguous) {
			t.Fatalf("shared label hid distinct cluster IDs: %v", err)
		}
	}
	promtest.Run(t, image, input, tests)
}

func TestPVCUsageHandlerUsesNamespaceCacheProof(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		localPresent, priorEmpty bool
	}{
		{name: "local stats", localPresent: true},
		{name: "foreign-only stats"},
		{name: "local stats after empty workload probe", localPresent: true, priorEmpty: true},
		{name: "foreign-only stats after empty workload probe", priorEmpty: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := fake.NewClientset(
				&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "db", Name: "pg-0", UID: "local-uid"}},
				&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "other", Name: "other-pod", UID: "other-uid"}},
			)
			if err := k8s.InitScopedTestResourceCache(cs, map[string]k8score.ResourceScope{string(k8score.Pods): {Enabled: true}}); err != nil {
				t.Fatal(err)
			}
			defer k8s.ResetResourceCache()
			proofs := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				query := r.Form.Get("query")
				if query == "up" {
					_, _ = w.Write([]byte(authProbeBody))
					return
				}
				if strings.Contains(query, "kube_pod_info") {
					proofs++
					if strings.Contains(query, `pod=~"new-0"`) {
						_, _ = w.Write(attributionEvidence(nil, ""))
						return
					}
					if !strings.Contains(query, `namespace="db"`) || !strings.Contains(query, `pod=~"pg-0"`) || strings.Contains(query, "other-pod") {
						t.Errorf("proof escaped claim namespace: %s", query)
					}
					_, _ = w.Write(attributionEvidence([]prom.Series{{Labels: map[string]string{"namespace": "db", "pod": "pg-0", "uid": "local-uid"}}}, ""))
					return
				}
				if strings.HasPrefix(query, "max(count by (") {
					_, _ = w.Write(attributionEvidence([]prom.Series{vec(nil, 1)}, ""))
					return
				}
				unlabeled := true
				for _, label := range partitionLabels {
					unlabeled = unlabeled && strings.Contains(query, label+`=""`)
				}
				if strings.HasPrefix(query, "topk(2,") {
					rows := []prom.Series{}
					if !unlabeled || tc.localPresent {
						labels := map[string]string{}
						if !unlabeled {
							labels["cluster"] = "foreign"
						}
						rows = append(rows, vec(labels, 1))
					}
					_, _ = w.Write(attributionEvidence(rows, ""))
					return
				}
				value := 1000
				if strings.Contains(query, "used_bytes") {
					value = 95
				}
				if unlabeled {
					value = 100
					if strings.Contains(query, "used_bytes") {
						value = 95
					}
				}
				result := "[]"
				if !unlabeled || tc.localPresent {
					if strings.HasPrefix(query, "count(") {
						value = 1
					}
					result = fmt.Sprintf(`[{"metric":{"persistentvolumeclaim":"pg-data"},"value":[100,"%d"]}]`, value)
				}
				_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":%s}}`, result)
			}))
			defer upstream.Close()
			Initialize(nil, nil, "cache-proof")
			SetManualURL(upstream.URL)
			SetAuthGate(func(_ *http.Request, group, resource, namespace, verb string) bool {
				return group == "" && resource == "persistentvolumeclaims" && namespace == "db" && verb == "get"
			})
			defer func() { SetAuthGate(nil); Reset(); Initialize(nil, nil, "") }()
			wantProofs := 1
			if tc.priorEmpty {
				wantProofs++
				proof, err := GetClient().historicalClusterScope(context.Background(), PodScope{Namespace: "db", Identities: []prom.WorkloadPodIdentity{{Name: "new-0", UID: "new-uid"}}}, nil)
				if err != nil || len(proof.ClusterLabels) != 0 {
					t.Fatalf("unrelated workload probe: %+v err=%v", proof, err)
				}
			}
			w := httptest.NewRecorder()
			metricsRouter().ServeHTTP(w, httptest.NewRequest("GET", "/prometheus/pvc/db/pg-data", nil))
			var response PVCUsageResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || proofs != wantProofs {
				t.Fatalf("status=%d proofs=%d body=%s", w.Code, proofs, w.Body.String())
			}
			if tc.localPresent {
				if response.Status != PVCUsageAvailable || !response.HasData || response.Used != 95 || response.Capacity != 100 || response.Ratio != 0.95 {
					t.Fatalf("cache-backed proof selected foreign values: %+v", response)
				}
			} else if response.Status != PVCUsageScopeMismatch || response.HasData {
				t.Fatalf("cache-backed proof accepted foreign-only stats: %+v", response)
			}
		})
	}
}

func TestPVCScopeRefusesResidualAndBatchPartitions(t *testing.T) {
	for _, verified := range []bool{false, true} {
		for _, window := range []time.Duration{0, time.Hour} {
			claims := []string{"disk"}
			var labels map[string]string
			if verified {
				labels = map[string]string{"cluster": "shared"}
			} else {
				claims = make([]string, pvcUsageBatchSize+1)
				for i := range claims {
					claims[i] = fmt.Sprintf("claim%03d", i)
				}
			}
			q := &fakeScanQuerier{queryFn: func(query string) (*prom.QueryResult, error) {
				rows := []prom.Series{vec(nil, 1)}
				if strings.HasPrefix(query, "topk(2,") {
					rows = []prom.Series{vec(map[string]string{"cluster": "shared", "cluster_id": "east"}, 1)}
					if verified {
						rows = append(rows, vec(map[string]string{"cluster": "shared", "cluster_id": "west"}, 1))
					} else if strings.Contains(query, "claim100") {
						rows[0].Labels["cluster_id"] = "west"
					}
				}
				return &prom.QueryResult{Series: rows}, nil
			}}
			if _, _, err := decideScope(context.Background(), q, pvcScopeProbe("db", claims, window), labels); !errors.Is(err, ErrScopeAmbiguous) {
				t.Fatalf("mixed partition accepted (verified=%t, window=%s): %v", verified, window, err)
			}
		}
	}
}
