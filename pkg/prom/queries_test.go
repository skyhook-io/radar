package prom

import (
	"strings"
	"testing"
)

func TestBuildPodSetQueryMatchesExactPodNames(t *testing.T) {
	pods := []string{"api-7d9f-abc12", "api-7d9f-def34"}
	want := `pod=~'^(api-7d9f-abc12|api-7d9f-def34)$'`
	for _, cat := range []MetricCategory{CategoryCPU, CategoryMemory, CategoryRestarts} {
		q := BuildPodSetQuery("shop", pods, cat)
		if !strings.Contains(q, want) {
			t.Errorf("%s: query %q lacks anchored pod set %q", cat, q, want)
		}
		if !strings.Contains(q, `namespace='shop'`) {
			t.Errorf("%s: query %q lacks namespace matcher", cat, q)
		}
		if strings.Contains(q, "api-.*") {
			t.Errorf("%s: query %q uses the workload prefix pattern", cat, q)
		}
		if !strings.HasPrefix(q, "sum(") || strings.HasSuffix(q, "by (pod,namespace)") {
			t.Errorf("%s: query %q splits the set per pod instead of one sum", cat, q)
		}
	}
}

func TestBuildPodSetQueryReusesCategoryExpressions(t *testing.T) {
	pods := []string{"web-0"}
	cases := map[MetricCategory]string{
		CategoryCPU:      `sum(rate(container_cpu_usage_seconds_total{container!='',namespace='ns',pod=~'^(web-0)$'}[5m]))`,
		CategoryMemory:   `sum(max by (pod,namespace,container) (container_memory_working_set_bytes{container!='',namespace='ns',pod=~'^(web-0)$'}))`,
		CategoryRestarts: `sum(round(increase(kube_pod_container_status_restarts_total{namespace='ns',pod=~'^(web-0)$'}[1h])))`,
	}
	for cat, want := range cases {
		if got := BuildPodSetQuery("ns", pods, cat); got != want {
			t.Errorf("%s:\n got %s\nwant %s", cat, got, want)
		}
	}
	noFilter := BuildPodSetQueryNoContainerFilter("ns", pods, CategoryCPU)
	if strings.Contains(noFilter, "container!=") {
		t.Errorf("no-container-filter variant still filters: %s", noFilter)
	}
	if BuildPodSetQueryNoContainerFilter("ns", pods, CategoryRestarts) != cases[CategoryRestarts] {
		t.Error("restarts query should not change without the container filter")
	}
}

func TestBuildPodSetQueryRestartsReadCounterIncrease(t *testing.T) {
	q := BuildPodSetQuery("ns", []string{"web-0"}, CategoryRestarts)
	if strings.Contains(q, "changes(") {
		t.Fatalf("restarts query counts sample transitions, undercounting bursts between scrapes: %s", q)
	}
	if !strings.Contains(q, "round(increase(kube_pod_container_status_restarts_total") {
		t.Fatalf("restarts query should read the rounded counter increase: %s", q)
	}
}

func TestBuildPodSetQueryEscapesNames(t *testing.T) {
	q := BuildPodSetQuery("te'st", []string{"a.b", `c{d}`}, CategoryCPU)
	if !strings.Contains(q, `namespace='te\'st'`) {
		t.Errorf("namespace not sanitized: %s", q)
	}
	if !strings.Contains(q, `^(a\\.b|`) {
		t.Errorf("regex metacharacters not escaped: %s", q)
	}
	if strings.Contains(q, "c{d}") {
		t.Errorf("label-matcher characters not sanitized: %s", q)
	}
}

func TestBuildPodSetQueryEmptyCases(t *testing.T) {
	if q := BuildPodSetQuery("ns", nil, CategoryCPU); q != "" {
		t.Errorf("empty pod set should yield no query, got %q", q)
	}
	if q := BuildPodSetQuery("ns", []string{"a"}, CategoryNetworkRX); q != "" {
		t.Errorf("unsupported category should yield no query, got %q", q)
	}
}
