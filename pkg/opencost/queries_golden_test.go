package opencost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/skyhook-io/radar/pkg/prom"
)

// The golden file holds the exact cost queries each metrics-store compute
// path sent before cluster scoping existed. A single-cluster store, whose
// filters are all empty, must keep receiving them byte for byte.

const goldenCostNamespace = "pay-ments"

// goldenStore answers every query with one sample carrying the labels the
// compute paths group by, so every branch that depends on a non-empty answer
// runs. With failAllocations, the allocation queries fail and the fallbacks
// run instead.
type goldenStore struct {
	mu              sync.Mutex
	queries         []string
	failAllocations bool
}

func (s *goldenStore) client(t *testing.T) *prom.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.FormValue("query")
		s.mu.Lock()
		s.queries = append(s.queries, r.URL.Path+" "+strconv.Quote(q))
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if s.failAllocations && (strings.Contains(q, "container_cpu_allocation") || strings.Contains(q, "container_memory_allocation_bytes")) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"status":"error","errorType":"execution","error":"boom"}`))
			return
		}
		labels := `{"namespace":"` + goldenCostNamespace + `","pod":"api-1","node":"n1","owner_kind":"Deployment","owner_name":"api"}`
		if r.URL.Path == "/api/v1/query_range" {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":` + labels + `,"values":[[1700000000,"1"],[1700003600,"1"]]}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":` + labels + `,"value":[1700000000,"1"]}]}}`))
	}))
	t.Cleanup(srv.Close)
	return prom.NewClient(prom.NewHTTPTransport(srv.URL, "", nil))
}

func (s *goldenStore) take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.queries
	s.queries = nil
	return out
}

type costGoldenCase struct {
	name            string
	failAllocations bool
	run             func(context.Context, *prom.Client)
}

var goldenAppRefs = []ApplicationWorkloadRef{
	{Kind: "Deployment", Namespace: goldenCostNamespace, Name: "api"},
	{Kind: "StatefulSet", Namespace: goldenCostNamespace, Name: "db"},
	{Kind: "DaemonSet", Namespace: goldenCostNamespace, Name: "agent"},
}

func costGoldenCases() []costGoldenCase {
	summary := func(skip bool) func(context.Context, *prom.Client) {
		return func(ctx context.Context, c *prom.Client) {
			ComputeCostSummaryFromProm(ctx, c, SummaryOptions{SkipNodeCost: skip})
		}
	}
	workloads := func(ctx context.Context, c *prom.Client) {
		ComputeWorkloadsFromProm(ctx, c, goldenCostNamespace, nil)
	}
	workloadTrend := func(kind string) func(context.Context, *prom.Client) {
		return func(ctx context.Context, c *prom.Client) {
			ComputeWorkloadCostTrendFromProm(ctx, c, WorkloadTrendOptions{Range: "24h", Namespace: goldenCostNamespace, Kind: kind, Name: "api"})
		}
	}
	appTrend := func(ctx context.Context, c *prom.Client) {
		ComputeApplicationCostTrendFromProm(ctx, c, ApplicationTrendOptions{Range: "24h", Workloads: goldenAppRefs})
	}
	return []costGoldenCase{
		{name: "summary", run: summary(false)},
		{name: "summary-skip-node-cost", run: summary(true)},
		{name: "summary-fallback", failAllocations: true, run: summary(false)},
		{name: "workloads", run: workloads},
		{name: "workloads-fallback", failAllocations: true, run: workloads},
		{name: "nodes", run: func(ctx context.Context, c *prom.Client) { ComputeNodeCosts(ctx, c) }},
		{name: "trend", run: func(ctx context.Context, c *prom.Client) {
			ComputeCostTrendFromProm(ctx, c, TrendPromOptions{Range: "24h"})
		}},
		{name: "workload-trend-deployment", run: workloadTrend("Deployment")},
		{name: "workload-trend-statefulset", run: workloadTrend("StatefulSet")},
		{name: "workload-trend-fallback", failAllocations: true, run: workloadTrend("Deployment")},
		{name: "application-trend", run: appTrend},
		{name: "application-trend-fallback", failAllocations: true, run: appTrend},
	}
}

func costGoldenLines(t *testing.T, cases []costGoldenCase) []string {
	t.Helper()
	var lines []string
	for _, tc := range cases {
		store := &goldenStore{failAllocations: tc.failAllocations}
		tc.run(context.Background(), store.client(t))
		for _, q := range store.take() {
			lines = append(lines, tc.name+"\t"+q)
		}
	}
	return lines
}

func checkCostGolden(t *testing.T, lines []string) {
	t.Helper()
	got := strings.Join(lines, "\n") + "\n"
	path := filepath.Join("testdata", "cost_queries_golden.txt")
	if os.Getenv("RADAR_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("cost queries changed\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestCostQueriesGolden(t *testing.T) {
	checkCostGolden(t, costGoldenLines(t, costGoldenCases()))
}
