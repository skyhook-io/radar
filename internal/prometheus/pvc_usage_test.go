package prometheus

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
