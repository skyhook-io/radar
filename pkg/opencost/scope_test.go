package opencost

import (
	"context"
	"sort"
	"testing"

	"github.com/skyhook-io/radar/pkg/prom"
)

// trendProm serves a matrix response with two data points per namespace.
func trendProm(t *testing.T, values map[string][]float64) *prom.Client {
	t.Helper()
	names := make([]string, 0, len(values))
	for ns := range values {
		names = append(names, ns)
	}
	sort.Strings(names)
	series := make([]namespaceSeries, 0, len(names))
	for _, ns := range names {
		points := make([]dpoint, 0, len(values[ns]))
		for i, v := range values[ns] {
			points = append(points, dpoint{ts: 1700000000 + int64(i)*3600, v: v})
		}
		series = append(series, namespaceSeries{ns: ns, points: points})
	}
	return rangeProm(t, matrixBody(series))
}

// summaryScopeCases serves three namespaces plus a node bill that exceeds
// their sum, so a restricted response that only dropped rows would still leak
// the cluster figure through the node-total ceiling.
func summaryScopeCases() []scriptedCase {
	return []scriptedCase{
		{contains: "container_cpu_allocation", body: vectorBody(map[string]float64{"team-a": 1.0, "team-b": 2.0, "kube-system": 0.5})},
		{contains: "container_memory_allocation_bytes", body: vectorBody(map[string]float64{"team-a": 1.5, "team-b": 1.0, "kube-system": 0.5})},
		{contains: "container_cpu_usage_seconds_total", body: vectorBody(map[string]float64{"team-a": 0.5, "team-b": 1.0, "kube-system": 0.25})},
		{contains: "container_memory_working_set_bytes", body: vectorBody(map[string]float64{"team-a": 0.75, "team-b": 0.5, "kube-system": 0.25})},
		{contains: "pv_hourly_cost", body: vectorBody(map[string]float64{"team-a": 0.5})},
		{contains: "node_total_hourly_cost", body: scalarBody(20.0)},
	}
}

func namespaceRow(t *testing.T, s *CostSummary, name string) NamespaceCost {
	t.Helper()
	for _, row := range s.Namespaces {
		if row.Name == name {
			return row
		}
	}
	t.Fatalf("namespace %q missing from %+v", name, s.Namespaces)
	return NamespaceCost{}
}

func TestComputeCostSummaryFromProm_UnrestrictedKeepsClusterView(t *testing.T) {
	client := scriptedProm(t, summaryScopeCases())

	got := ComputeCostSummaryFromProm(context.Background(), client, SummaryOptions{Currency: "USD"})
	if !got.Available {
		t.Fatalf("unavailable: %+v", got)
	}
	if len(got.Namespaces) != 3 {
		t.Errorf("got %d namespaces, want 3", len(got.Namespaces))
	}
	if got.Restricted {
		t.Error("Restricted = true, want false")
	}
	if got.TotalHourlyCost != 20 {
		t.Errorf("TotalHourlyCost = %v, want 20 (the node-total ceiling)", got.TotalHourlyCost)
	}
}

func TestComputeCostSummaryFromProm_RestrictedDropsOtherNamespaces(t *testing.T) {
	client := scriptedProm(t, summaryScopeCases())

	got := ComputeCostSummaryFromProm(context.Background(), client, SummaryOptions{
		Currency:          "USD",
		AllowedNamespaces: []string{"team-a"},
	})
	if !got.Available {
		t.Fatalf("unavailable: %+v", got)
	}
	if !got.Restricted {
		t.Error("Restricted = false, want true")
	}
	if len(got.Namespaces) != 1 || got.Namespaces[0].Name != "team-a" {
		t.Fatalf("namespaces = %+v, want only team-a", got.Namespaces)
	}
	row := namespaceRow(t, got, "team-a")
	if row.HourlyCost != 3.0 { // 1.0 cpu + 1.5 mem + 0.5 storage
		t.Errorf("team-a HourlyCost = %v, want 3.0", row.HourlyCost)
	}
}

// The reported leak: a namespace-scoped user was shown the cluster total. The
// node-total ceiling is a cluster figure and must not survive the restriction.
func TestComputeCostSummaryFromProm_RestrictedTotalsCoverOnlyVisibleRows(t *testing.T) {
	client := scriptedProm(t, summaryScopeCases())

	got := ComputeCostSummaryFromProm(context.Background(), client, SummaryOptions{
		Currency:          "USD",
		AllowedNamespaces: []string{"team-a"},
	})
	if got.TotalHourlyCost != 3.0 {
		t.Errorf("TotalHourlyCost = %v, want 3.0 (the team-a row, not the 20.0 node bill)", got.TotalHourlyCost)
	}
	if got.TotalStorageCost != 0.5 {
		t.Errorf("TotalStorageCost = %v, want 0.5", got.TotalStorageCost)
	}
	// alloc = 1.0 + 1.5 = 2.5, usage = 0.5 + 0.75 = 1.25 → 50% efficient, 1.25 idle.
	if got.ClusterEfficiency != 50 {
		t.Errorf("ClusterEfficiency = %v, want 50", got.ClusterEfficiency)
	}
	if got.TotalIdleCost != 1.25 {
		t.Errorf("TotalIdleCost = %v, want 1.25", got.TotalIdleCost)
	}
}

func TestComputeCostSummaryFromProm_NoAllowedNamespacesIsDenied(t *testing.T) {
	client := scriptedProm(t, summaryScopeCases())

	got := ComputeCostSummaryFromProm(context.Background(), client, SummaryOptions{
		Currency:          "GBP",
		AllowedNamespaces: []string{},
	})
	if got.Available {
		t.Error("Available = true, want false")
	}
	if got.Reason != ReasonAccessDenied {
		t.Errorf("Reason = %q, want %q", got.Reason, ReasonAccessDenied)
	}
	if !got.Restricted {
		t.Error("Restricted = false, want true")
	}
	if len(got.Namespaces) != 0 || got.TotalHourlyCost != 0 {
		t.Errorf("denied summary still carries figures: %+v", got)
	}
	if got.Currency != "GBP" {
		t.Errorf("Currency = %q, want GBP preserved", got.Currency)
	}
}

// An in-scope namespace with no cost metrics is a real zero, not an error.
func TestComputeCostSummaryFromProm_AllowedNamespaceWithNoRowsStaysAvailable(t *testing.T) {
	client := scriptedProm(t, summaryScopeCases())

	got := ComputeCostSummaryFromProm(context.Background(), client, SummaryOptions{
		Currency:          "USD",
		AllowedNamespaces: []string{"team-z"},
	})
	if !got.Available {
		t.Errorf("Available = false (reason %q), want true", got.Reason)
	}
	if !got.Restricted {
		t.Error("Restricted = false, want true")
	}
	if got.TotalHourlyCost != 0 {
		t.Errorf("TotalHourlyCost = %v, want 0", got.TotalHourlyCost)
	}
}

func TestComputeCostTrendFromProm_RestrictedKeepsOnlyAllowedSeries(t *testing.T) {
	client := trendProm(t, map[string][]float64{
		"team-a":      {1, 2},
		"team-b":      {3, 4},
		"kube-system": {5, 6},
	})

	got := ComputeCostTrendFromProm(context.Background(), client, TrendPromOptions{
		AllowedNamespaces: []string{"team-a"},
	})
	if !got.Available {
		t.Fatalf("unavailable: %+v", got)
	}
	if len(got.Series) != 1 || got.Series[0].Namespace != "team-a" {
		t.Fatalf("series = %+v, want only team-a", got.Series)
	}
	if !got.Restricted {
		t.Error("Restricted = false, want true")
	}
}

// The "other" bucket aggregates whatever fell outside the top N. For a
// restricted caller it must aggregate only their own namespaces, or it becomes
// a second route to the cluster total.
func TestComputeCostTrendFromProm_RestrictedOtherBucketExcludesHiddenNamespaces(t *testing.T) {
	series := map[string][]float64{"secret-ns": {1000, 1000}}
	allowed := make([]string, 0, 10)
	for i := range 10 {
		name := string(rune('a'+i)) + "-team"
		series[name] = []float64{float64(i + 1), float64(i + 1)}
		allowed = append(allowed, name)
	}
	client := trendProm(t, series)

	got := ComputeCostTrendFromProm(context.Background(), client, TrendPromOptions{
		MaxSeries:         8,
		AllowedNamespaces: allowed,
	})

	var other *CostTrendSeries
	for i := range got.Series {
		if got.Series[i].Namespace == "other" {
			other = &got.Series[i]
		}
		if got.Series[i].Namespace == "secret-ns" {
			t.Fatal("a namespace outside the caller's scope was returned as its own series")
		}
	}
	if other == nil {
		t.Fatalf("expected an \"other\" bucket for the 2 allowed namespaces past the top 8; got %d series", len(got.Series))
	}
	for _, dp := range other.DataPoints {
		if dp.Value >= 1000 {
			t.Errorf("\"other\" bucket value %v includes the out-of-scope namespace", dp.Value)
		}
	}
}

func TestComputeCostTrendFromProm_NoAllowedNamespacesIsDenied(t *testing.T) {
	client := trendProm(t, map[string][]float64{"team-a": {1, 2}})

	got := ComputeCostTrendFromProm(context.Background(), client, TrendPromOptions{
		Range:             "7d",
		AllowedNamespaces: []string{},
	})
	if got.Available {
		t.Error("Available = true, want false")
	}
	if got.Reason != ReasonAccessDenied {
		t.Errorf("Reason = %q, want %q", got.Reason, ReasonAccessDenied)
	}
	if len(got.Series) != 0 {
		t.Errorf("Series = %+v, want none", got.Series)
	}
	if got.Range != "7d" {
		t.Errorf("Range = %q, want 7d echoed back", got.Range)
	}
}

// A denied answer still has to echo the same normalized label the query path
// would have produced, so the response contract does not fork by outcome.
func TestTrendRangeLabelNormalizesUnknownInput(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"6h", "6h"}, {"24h", "24h"}, {"7d", "7d"}, {"", "24h"}, {"weird", "24h"},
	} {
		if got := TrendRangeLabel(tt.in); got != tt.want {
			t.Errorf("TrendRangeLabel(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestComputeCostTrendFromProm_UnrestrictedIsUnchanged(t *testing.T) {
	client := trendProm(t, map[string][]float64{"team-a": {1, 2}, "team-b": {3, 4}})

	got := ComputeCostTrendFromProm(context.Background(), client, TrendPromOptions{})
	if len(got.Series) != 2 {
		t.Errorf("series = %+v, want both", got.Series)
	}
	if got.Restricted {
		t.Error("Restricted = true, want false")
	}
}
