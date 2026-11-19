package prom

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The golden file holds the exact chart and vitals queries the builders
// produced before cluster scoping existed. A single-cluster store, whose
// filters are all empty, must keep receiving them byte for byte.

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
