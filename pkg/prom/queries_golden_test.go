package prom

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The golden file pins the exact chart and vitals queries the builders
// produce with no cluster filter. A single-cluster store, whose filters are
// all empty, must keep receiving them byte for byte.

const goldenNamespace = "pay-ments"
const goldenPod = "api.x-1"
const goldenNode = "ip-10-0-0-1.ec2.internal"

var goldenPods = []string{"api.x-1", "api-2"}

func chartGoldenLines() []string {
	var lines []string
	add := func(name, query string) {
		lines = append(lines, name+"\t"+query)
	}
	for _, cat := range AllCategories() {
		c := string(cat)
		add("pod/"+c, BuildQuery("Pod", goldenNamespace, goldenPod, cat))
		add("pod-nocf/"+c, BuildQueryNoContainerFilter("Pod", goldenNamespace, goldenPod, cat))
		add("node/"+c, BuildQuery("Node", "", goldenNode, cat))
		add("namespace/"+c, BuildNamespaceQuery(goldenNamespace, cat))
		add("namespace-nocf/"+c, BuildNamespaceQueryNoContainerFilter(goldenNamespace, cat))
		add("cluster/"+c, BuildClusterQuery(cat))
		add("cluster-nocf/"+c, BuildClusterQueryNoContainerFilter(cat))
		sel := SelectPods(goldenNamespace, goldenPods)
		add("scoped-perpod/"+c, BuildScopedQuery(sel, cat, AggregatePerPod, true))
		add("scoped-perpod-nocf/"+c, BuildScopedQuery(sel, cat, AggregatePerPod, false))
		add("scoped-total/"+c, BuildScopedQuery(sel, cat, AggregateTotal, true))
		add("scoped-total-nocf/"+c, BuildScopedQuery(sel, cat, AggregateTotal, false))
		add("scoped-empty/"+c, BuildScopedQuery(SelectPods(goldenNamespace, nil), cat, AggregatePerPod, true))
		add("podset/"+c, BuildPodSetQuery(goldenNamespace, goldenPods, cat))
		add("podset-nocf/"+c, BuildPodSetQueryNoContainerFilter(goldenNamespace, goldenPods, cat))
	}
	current, desired := BuildHPAReplicasQueries(goldenNamespace, "api-hpa")
	add("hpa/current", current)
	add("hpa/desired", desired)
	return lines
}

func checkChartGolden(t *testing.T, lines []string) {
	t.Helper()
	got := strings.Join(lines, "\n") + "\n"
	path := filepath.Join("testdata", "charts_golden.txt")
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
		t.Fatalf("chart queries changed\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestChartQueriesGolden(t *testing.T) {
	checkChartGolden(t, chartGoldenLines())
}

// filteredChartLines builds the golden set through the filtered builders.
func filteredChartLines(filter ClusterFilter) []string {
	var lines []string
	add := func(name, query string) {
		lines = append(lines, name+"\t"+query)
	}
	for _, cat := range AllCategories() {
		c := string(cat)
		add("pod/"+c, BuildQueryFiltered("Pod", goldenNamespace, goldenPod, cat, true, filter))
		add("pod-nocf/"+c, BuildQueryFiltered("Pod", goldenNamespace, goldenPod, cat, false, filter))
		add("node/"+c, BuildQueryFiltered("Node", "", goldenNode, cat, true, filter))
		add("namespace/"+c, BuildNamespaceQueryFiltered(goldenNamespace, cat, true, filter))
		add("namespace-nocf/"+c, BuildNamespaceQueryFiltered(goldenNamespace, cat, false, filter))
		add("cluster/"+c, BuildClusterQueryFiltered(cat, true, filter))
		add("cluster-nocf/"+c, BuildClusterQueryFiltered(cat, false, filter))
		sel := SelectPods(goldenNamespace, goldenPods)
		add("scoped-perpod/"+c, BuildScopedQueryFiltered(sel, cat, AggregatePerPod, true, filter))
		add("scoped-perpod-nocf/"+c, BuildScopedQueryFiltered(sel, cat, AggregatePerPod, false, filter))
		add("scoped-total/"+c, BuildScopedQueryFiltered(sel, cat, AggregateTotal, true, filter))
		add("scoped-total-nocf/"+c, BuildScopedQueryFiltered(sel, cat, AggregateTotal, false, filter))
		add("scoped-empty/"+c, BuildScopedQueryFiltered(SelectPods(goldenNamespace, nil), cat, AggregatePerPod, true, filter))
		add("podset/"+c, BuildPodSetQuery(goldenNamespace, goldenPods, cat))
		add("podset-nocf/"+c, BuildPodSetQueryNoContainerFilter(goldenNamespace, goldenPods, cat))
	}
	current, desired := BuildHPAReplicasQueriesFiltered(goldenNamespace, "api-hpa", filter)
	add("hpa/current", current)
	add("hpa/desired", desired)
	return lines
}

func TestFilteredChartQueriesUnfilteredAreGolden(t *testing.T) {
	checkChartGolden(t, filteredChartLines(nil))
	checkChartGolden(t, filteredChartLines(func(string) string { return "" }))
}

// selectorMatchers returns every metric selector in a query with its matchers.
func selectorMatchers(query string) map[string][]string {
	out := map[string][]string{}
	for _, m := range regexp.MustCompile(`([a-zA-Z_:]+)(\{([^}]*)\})?`).FindAllStringSubmatch(query, -1) {
		name := m[1]
		if !strings.Contains(name, "_") || strings.HasSuffix(name, "_over_time") {
			continue
		}
		out[name] = append(out[name], m[3])
	}
	return out
}

func TestFilteredChartQueriesCarryEachMetricsFilter(t *testing.T) {
	filter := func(metric string) string { return `cluster="` + metric + `"` }
	sel := SelectPods(goldenNamespace, goldenPods)
	type built struct {
		kind  string
		query string
		cat   MetricCategory
	}
	var all []built
	for _, cat := range AllCategories() {
		for _, cf := range []bool{true, false} {
			all = append(all,
				built{"Pod", BuildQueryFiltered("Pod", goldenNamespace, goldenPod, cat, cf, filter), cat},
				built{"Node", BuildQueryFiltered("Node", "", goldenNode, cat, cf, filter), cat},
				built{"", BuildNamespaceQueryFiltered(goldenNamespace, cat, cf, filter), cat},
				built{"", BuildClusterQueryFiltered(cat, cf, filter), cat},
				built{"Deployment", BuildScopedQueryFiltered(sel, cat, AggregatePerPod, cf, filter), cat},
				built{"Deployment", BuildScopedQueryFiltered(sel, cat, AggregateTotal, cf, filter), cat},
			)
		}
	}
	checked := 0
	for _, b := range all {
		if b.query == "" {
			continue
		}
		selectors := selectorMatchers(b.query)
		want := CategoryMetrics(b.kind, b.cat)
		if len(selectors) != len(want) {
			t.Errorf("%s/%s reads %v, CategoryMetrics says %v: %s", b.kind, b.cat, selectors, want, b.query)
		}
		for _, metric := range want {
			if len(selectors[metric]) == 0 {
				t.Errorf("%s/%s lacks %s: %s", b.kind, b.cat, metric, b.query)
			}
			for _, m := range selectors[metric] {
				checked++
				if !strings.HasSuffix(m, filter(metric)) {
					t.Errorf("%s selector {%s} misses its filter in %s", metric, m, b.query)
				}
			}
		}
	}
	current, desired := BuildHPAReplicasQueriesFiltered(goldenNamespace, "api-hpa", filter)
	for metric, q := range map[string]string{MetricHPACurrentReplicas: current, MetricHPADesiredReplicas: desired} {
		if !strings.HasSuffix(q, ","+filter(metric)+"}") {
			t.Errorf("%s misses its filter: %s", metric, q)
		}
	}
	if checked < 50 {
		t.Fatalf("checked only %d selectors", checked)
	}
}
