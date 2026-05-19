package traffic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Guards the parallel applyHeaders implementation in caretta.go against
// silent drift from internal/prometheus/client.go — a future contributor
// adding a 4th Prometheus call site here won't have a failing test to
// remind them if this one is missing.
func TestCarettaAppliesHeaders(t *testing.T) {
	var gotAuth, gotOrg atomic.Value
	gotAuth.Store("")
	gotOrg.Store("")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		gotOrg.Store(r.Header.Get("X-Scope-OrgID"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer srv.Close()

	c := &CarettaSource{
		httpClient:     &http.Client{Timeout: 5 * time.Second},
		prometheusAddr: srv.URL,
		headers: map[string]string{
			"Authorization": "Bearer caretta-token",
			"X-Scope-OrgID": "tenant-42",
		},
	}

	if _, err := c.queryPrometheusRaw(context.Background(), "up"); err != nil {
		t.Fatalf("queryPrometheusRaw failed: %v", err)
	}
	if got := gotAuth.Load().(string); got != "Bearer caretta-token" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer caretta-token")
	}
	if got := gotOrg.Load().(string); got != "tenant-42" {
		t.Errorf("X-Scope-OrgID = %q, want %q", got, "tenant-42")
	}

	// tryMetricsEndpointLocked must carry the same headers, otherwise
	// discovery would 401 against an auth-protected endpoint and the user
	// would see "no metrics source found". Acquire the write lock first —
	// that's what every production caller does, and the function name's
	// "Locked" suffix is the Go convention saying so. A previous version
	// of applyHeaders took c.mu.RLock and deadlocked here.
	gotAuth.Store("")
	c.mu.Lock()
	ok := c.tryMetricsEndpointLocked(context.Background(), srv.URL)
	c.mu.Unlock()
	if !ok {
		t.Fatal("tryMetricsEndpointLocked returned false for healthy server")
	}
	if got := gotAuth.Load().(string); got != "Bearer caretta-token" {
		t.Errorf("probe Authorization = %q, want %q", got, "Bearer caretta-token")
	}
}
