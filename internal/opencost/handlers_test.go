package opencost

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	pkgopencost "github.com/skyhook-io/radar/pkg/opencost"
	"github.com/skyhook-io/radar/pkg/prom"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
)

func TestUnavailableResponsesIncludeCurrency(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		handler func(http.ResponseWriter, *http.Request, func() string, RouteScope)
	}{
		{name: "summary", target: "/opencost/summary", handler: handleSummaryScoped},
		{name: "workloads", target: "/opencost/workloads?namespace=default", handler: handleWorkloadsScoped},
		{name: "trend", target: "/opencost/trend", handler: handleTrendScoped},
		{name: "nodes", target: "/opencost/nodes", handler: handleNodesScoped},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			w := httptest.NewRecorder()
			tt.handler(w, req, func() string { return "GBP" }, RouteScope{})

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

func TestBuildPodOwnerLookupTreatsEmptyNamespaceAsConclusive(t *testing.T) {
	if err := k8s.InitTestResourceCache(fakeclientset.NewSimpleClientset()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetResourceCache)

	lookup := BuildPodOwnerLookup("empty")
	if lookup == nil {
		t.Fatal("empty pod list returned an inconclusive nil lookup")
	}
	if _, found := lookup("old-pod"); found {
		t.Fatal("empty pod list reported a historical pod as live")
	}
}

func TestCostRoutesResolveCurrencyAfterSourceSelection(t *testing.T) {
	originalConfig := ConfigSnapshot()
	t.Cleanup(func() { _ = Configure(originalConfig) })

	tests := []struct {
		name    string
		target  string
		handler func(http.ResponseWriter, *http.Request, func() string, RouteScope)
	}{
		{name: "summary", target: "/opencost/summary", handler: handleSummaryScoped},
		{name: "workloads", target: "/opencost/workloads?namespace=default", handler: handleWorkloadsScoped},
		{name: "trend", target: "/opencost/trend", handler: handleTrendScoped},
		{name: "nodes", target: "/opencost/nodes", handler: handleNodesScoped},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Configure(ManagerConfig{Source: SourcePrometheus}); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			tt.handler(w, httptest.NewRequest(http.MethodGet, tt.target, nil), func() string {
				if selected := SelectedSourceSnapshot(); selected != SourcePrometheus {
					t.Fatalf("currency resolved before source selection: selected = %q", selected)
				}
				return "GBP"
			}, RouteScope{})
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
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
	for _, route := range []struct {
		name   string
		target string
		call   func(http.ResponseWriter, *http.Request, func() string, RouteScope)
	}{
		{name: "summary", target: "/opencost/summary", call: handleSummaryScoped},
		{name: "trend", target: "/opencost/trend?range=24h", call: handleTrendScoped},
	} {
		t.Run(route.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			route.call(recorder, httptest.NewRequest(http.MethodGet, route.target, nil), func() string { return "GBP" }, scope)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			var body struct {
				Available bool   `json:"available"`
				Reason    string `json:"reason"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Available || body.Reason != pkgopencost.ReasonAccessDenied {
				t.Fatalf("unexpected response: %#v", body)
			}
		})
	}
}

func TestWorkloadCostAuthorizesSingularNamespaceDirectly(t *testing.T) {
	scope := RouteScope{
		AllowedNamespaces: func(_ *http.Request, requested []string) []string {
			if len(requested) == 1 && requested[0] == "my-team" {
				return requested
			}
			return []string{}
		},
	}
	recorder := httptest.NewRecorder()
	handleWorkloadsScoped(
		recorder,
		httptest.NewRequest(http.MethodGet, "/opencost/workloads?namespace=finance-prod&namespaces=my-team", nil),
		nil,
		scope,
	)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
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
	if len(resp.Namespaces) != 1 || resp.Namespaces[0].Name != "allowed" || resp.TotalHourlyCost != 3 || resp.ClusterEfficiency != 50 || len(resp.NamespaceScope) != 1 || resp.NamespaceScope[0] != "allowed" {
		t.Fatalf("unexpected filtered summary: %#v", resp)
	}
}

func TestFilterCostSummaryReportsNoMetricsForVisibleNamespacesWithoutRows(t *testing.T) {
	resp := &pkgopencost.CostSummary{
		Available:  true,
		Namespaces: []pkgopencost.NamespaceCost{{Name: "private", HourlyCost: 11}},
	}
	filterCostSummary(resp, []string{"allowed"})
	if resp.Available || resp.Reason != pkgopencost.ReasonNoMetrics || len(resp.Namespaces) != 0 || len(resp.NamespaceScope) != 1 || resp.NamespaceScope[0] != "allowed" {
		t.Fatalf("unexpected empty visible summary: %#v", resp)
	}
}

func TestConnectionSelectionFailuresDoNotClaimKubecostSource(t *testing.T) {
	originalConfig := ConfigSnapshot()
	t.Cleanup(func() { _ = Configure(originalConfig) })
	defaultManager.mu.Lock()
	defaultManager.selectionErr = errors.New("cost source selection was superseded")
	defaultManager.retryAt = time.Now().Add(time.Minute)
	defaultManager.selected = ""
	defaultManager.mu.Unlock()

	recorder := httptest.NewRecorder()
	handleSummaryScoped(recorder, httptest.NewRequest(http.MethodGet, "/opencost/summary", nil), nil, RouteScope{})
	var body struct {
		Source string `json:"source"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Source != "" || body.Reason != pkgopencost.ReasonQueryError {
		t.Fatalf("unexpected selection failure response: %#v", body)
	}
}

func TestConnectionFailureReasonRecognizesHTTPAuthentication(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		if got := ConnectionFailureReason(&prom.HTTPError{StatusCode: status}); got != pkgopencost.ReasonAuthentication {
			t.Fatalf("status %d reason = %q, want %q", status, got, pkgopencost.ReasonAuthentication)
		}
	}
}

func TestConnectionFailureReasonRecognizesNoCostSource(t *testing.T) {
	err := fmt.Errorf("selection failed: %w", ErrNoCostSource)
	if got := ConnectionFailureReason(err); got != pkgopencost.ReasonNoCostSource {
		t.Fatalf("reason = %q, want %q", got, pkgopencost.ReasonNoCostSource)
	}
}

func TestConnectionFailureReasonDoesNotGuessAuthenticationFromText(t *testing.T) {
	err := &prom.HTTPError{StatusCode: http.StatusBadGateway, Body: []byte("authentication service unavailable")}
	if got := ConnectionFailureReason(err); got != pkgopencost.ReasonQueryError {
		t.Fatalf("reason = %q, want %q", got, pkgopencost.ReasonQueryError)
	}
	if got := ConnectionFailureReason(ErrKubecostAuthentication); got != pkgopencost.ReasonAuthentication {
		t.Fatalf("typed Kubecost auth reason = %q, want %q", got, pkgopencost.ReasonAuthentication)
	}
	if got := ConnectionFailureReason(ErrKubecostContextMismatch); got != pkgopencost.ReasonConfigMismatch {
		t.Fatalf("context mismatch reason = %q, want %q", got, pkgopencost.ReasonConfigMismatch)
	}
	if got := ConnectionFailureReason(fmt.Errorf("%w: invalid source", ErrCostSourceEnvConfig)); got != pkgopencost.ReasonDeploymentConfig {
		t.Fatalf("environment config reason = %q, want %q", got, pkgopencost.ReasonDeploymentConfig)
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
		handler func(http.ResponseWriter, *http.Request, func() string, RouteScope)
	}{
		{name: "summary", target: "/opencost/summary", handler: handleSummaryScoped},
		{name: "workloads", target: "/opencost/workloads?namespace=default", handler: handleWorkloadsScoped},
		{name: "trend", target: "/opencost/trend", handler: handleTrendScoped},
		{name: "nodes", target: "/opencost/nodes", handler: handleNodesScoped},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tt.handler(w, httptest.NewRequest(http.MethodGet, tt.target, nil), func() string { return "GBP" }, RouteScope{})

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
