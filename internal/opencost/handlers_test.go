package opencost

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	pkgopencost "github.com/skyhook-io/radar/pkg/opencost"
	"github.com/skyhook-io/radar/pkg/prom"
)

func TestUnavailableResponsesIncludeCurrency(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		handler func(http.ResponseWriter, *http.Request, func() string)
	}{
		{name: "summary", target: "/opencost/summary", handler: handleSummary},
		{name: "workloads", target: "/opencost/workloads?namespace=default", handler: handleWorkloads},
		{name: "trend", target: "/opencost/trend", handler: handleTrend},
		{name: "nodes", target: "/opencost/nodes", handler: handleNodes},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			w := httptest.NewRecorder()
			tt.handler(w, req, func() string { return "GBP" })

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			var body struct {
				Currency string `json:"currency"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Currency != "GBP" {
				t.Errorf("currency = %q, want GBP", body.Currency)
			}
		})
	}
}

func TestCostRouteScopeRejectsUnreadableNamespaceAndNodes(t *testing.T) {
	scope := RouteScope{
		AllowedNamespaces: func(_ *http.Request, _ []string) []string { return []string{} },
		CanReadNodes:      func(_ *http.Request) bool { return false },
	}
	workloads := httptest.NewRecorder()
	handleWorkloadsScoped(workloads, httptest.NewRequest(http.MethodGet, "/opencost/workloads?namespace=private", nil), nil, scope)
	if workloads.Code != http.StatusForbidden {
		t.Fatalf("workloads status = %d, want 403", workloads.Code)
	}
	nodes := httptest.NewRecorder()
	handleNodesScoped(nodes, httptest.NewRequest(http.MethodGet, "/opencost/nodes", nil), nil, scope)
	if nodes.Code != http.StatusForbidden {
		t.Fatalf("nodes status = %d, want 403", nodes.Code)
	}
}

func TestFilterCostSummaryRecomputesVisibleTotals(t *testing.T) {
	resp := &pkgopencost.CostSummary{
		Available: true,
		Namespaces: []pkgopencost.NamespaceCost{
			{Name: "allowed", HourlyCost: 3, CPUCost: 2, MemoryCost: 1, CPUUsageCost: 1, MemoryUsageCost: 0.5, IdleCost: 1.5},
			{Name: "private", HourlyCost: 8, CPUCost: 4, MemoryCost: 4, CPUUsageCost: 4, MemoryUsageCost: 4},
		},
	}
	filterCostSummary(resp, []string{"allowed"})
	if len(resp.Namespaces) != 1 || resp.Namespaces[0].Name != "allowed" || resp.TotalHourlyCost != 3 || resp.ClusterEfficiency != 50 {
		t.Fatalf("unexpected filtered summary: %#v", resp)
	}
}

func TestConnectionFailureReasonRecognizesHTTPAuthentication(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		if got := ConnectionFailureReason(&prom.HTTPError{StatusCode: status}); got != pkgopencost.ReasonAuthentication {
			t.Fatalf("status %d reason = %q, want %q", status, got, pkgopencost.ReasonAuthentication)
		}
	}
}

func TestConnectedResponsesIncludeCurrency(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("query") == "up" {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"job":"prometheus"},"value":[1700000000,"1"]}]}}`))
			return
		}
		resultType := "vector"
		if r.URL.Path == "/api/v1/query_range" {
			resultType = "matrix"
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"` + resultType + `","result":[]}}`))
	}))
	prometheuspkg.Initialize(nil, nil, "test")
	prometheuspkg.SetManualURL(srv.URL)
	t.Cleanup(func() {
		srv.Close()
		prometheuspkg.Reset()
		prometheuspkg.Initialize(nil, nil, "")
	})

	tests := []struct {
		name    string
		target  string
		handler func(http.ResponseWriter, *http.Request, func() string)
	}{
		{name: "summary", target: "/opencost/summary", handler: handleSummary},
		{name: "workloads", target: "/opencost/workloads?namespace=default", handler: handleWorkloads},
		{name: "trend", target: "/opencost/trend", handler: handleTrend},
		{name: "nodes", target: "/opencost/nodes", handler: handleNodes},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tt.handler(w, httptest.NewRequest(http.MethodGet, tt.target, nil), func() string { return "GBP" })

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			var body struct {
				Currency string `json:"currency"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Currency != "GBP" {
				t.Errorf("currency = %q, want GBP", body.Currency)
			}
		})
	}
}
