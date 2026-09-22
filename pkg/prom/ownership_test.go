package prom

import (
	"strings"
	"testing"
)

func TestBuildScopedQueryCurrentPodsMatchExactly(t *testing.T) {
	sel := SelectPods("shop", []string{"api-7f6-b", "api-7f6-a"})
	got := BuildScopedQuery(sel, CategoryCPU, AggregatePerPod, true)
	want := `sum(rate(container_cpu_usage_seconds_total{container!='',namespace='shop',pod=~'^(api-7f6-a|api-7f6-b)$'}[5m])) by (pod,namespace)`
	if got != want {
		t.Fatalf("cpu per pod:\n got %s\nwant %s", got, want)
	}
	if strings.Contains(got, "api-.*") {
		t.Fatalf("current-pods form must not prefix-match: %s", got)
	}
	escaped := BuildScopedQuery(SelectPods("te'st", []string{"a.b", "c{d}"}), CategoryCPU, AggregatePerPod, true)
	if strings.Contains(escaped, "a.b|") || strings.Contains(escaped, "c{d}") {
		t.Fatalf("names must be sanitized and regex-escaped: %s", escaped)
	}
	empty := BuildScopedQuery(PodSelection{Namespace: "shop"}, CategoryMemory, AggregateTotal, true)
	if !strings.Contains(empty, `pod=~'a^'`) {
		t.Fatalf("an empty selection must match nothing, got %s", empty)
	}
	if !(PodSelection{Namespace: "shop"}).IsEmpty() || sel.IsEmpty() {
		t.Fatal("IsEmpty disagrees with the selection")
	}
}

// Every category renders both aggregates, and no template loses a paren:
// an unbalanced expression is a query Prometheus rejects outright.
func TestBuildScopedQueryBalancesEveryCategory(t *testing.T) {
	sel := SelectPods("shop", []string{"api-7f6-a"})
	categories := []MetricCategory{CategoryCPU, CategoryMemory, CategoryRestarts, CategoryNetworkRX, CategoryNetworkTX, CategoryFilesystem}
	for _, category := range categories {
		for _, agg := range []Aggregate{AggregatePerPod, AggregateTotal} {
			for _, filterContainer := range []bool{true, false} {
				q := BuildScopedQuery(sel, category, agg, filterContainer)
				if q == "" {
					t.Fatalf("%s/%v: empty query", category, agg)
				}
				depth := 0
				for _, ch := range q {
					if ch == '(' {
						depth++
					} else if ch == ')' {
						depth--
						if depth < 0 {
							t.Fatalf("%s/%v: closes a paren it never opened: %s", category, agg, q)
						}
					}
				}
				if depth != 0 {
					t.Fatalf("%s/%v: %d unclosed parens: %s", category, agg, depth, q)
				}
				if !strings.Contains(q, `pod=~'^(api-7f6-a)$'`) {
					t.Fatalf("%s/%v: lost the pod scope: %s", category, agg, q)
				}
			}
		}
	}
}

func TestBuildQueryNoLongerInfersWorkloadPodsFromNames(t *testing.T) {
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob"} {
		if q := BuildQuery(kind, "shop", "api", CategoryCPU); q != "" {
			t.Errorf("%s: BuildQuery still builds from the name: %s", kind, q)
		}
	}
	if q := BuildQuery("Pod", "shop", "api-7f6-a", CategoryCPU); !strings.Contains(q, `pod='api-7f6-a'`) {
		t.Errorf("pod query lost its exact match: %s", q)
	}
}
