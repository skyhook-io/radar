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
	q := &fakeCNPGQuerier{instant: func(query string) (*prom.QueryResult, error) {
		east := strings.Contains(query, `cluster="east"`)
		switch {
		case strings.HasPrefix(query, "count by ("):
			return &prom.QueryResult{Series: []prom.Series{vec(map[string]string{"cluster": "east"}, 1), vec(map[string]string{"cluster": "west"}, 1)}}, nil
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
		if strings.HasPrefix(query, "count by (") {
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
