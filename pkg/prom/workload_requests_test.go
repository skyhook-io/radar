package prom

import (
	"strings"
	"testing"
	"time"
)

func TestRequestQueriesKeepSourceAndScope(t *testing.T) {
	for _, source := range []RequestSource{RequestSourceBeyla, RequestSourceIstio} {
		queries, err := BuildRequestQueries(time.Minute, SelectPods("shop", []string{"api-0"}), WorkloadMetricsScope{ClusterLabels: map[string]string{"cluster": "west"}}, source, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, query := range []string{queries.Rate, queries.Errors, queries.P50, queries.P95, queries.HistogramCoverage, queries.StatusCoverage, queries.ObservedPods} {
			if !strings.Contains(query, `cluster="west"`) || !strings.Contains(query, `^(api-0)$`) {
				t.Fatalf("%s lost cluster or exact pod scope: %s", source, query)
			}
			if source == RequestSourceIstio && (!strings.Contains(query, `reporter="destination"`) || !strings.Contains(query, `request_protocol="http"`)) {
				t.Fatalf("mixed observation/protocol population: %s", query)
			}
		}
		if !strings.Contains(queries.P95, "histogram_quantile(0.95, sum by (le)") {
			t.Fatalf("must aggregate buckets before quantile: %s", queries.P95)
		}
		if strings.HasSuffix(queries.P95, " / 1000") != (source == RequestSourceIstio) {
			t.Fatalf("wrong latency units for %s: %s", source, queries.P95)
		}
	}
}

func TestWorkloadRateWindowsCoverEvaluationSteps(t *testing.T) {
	for _, step := range []time.Duration{time.Minute, 20 * time.Minute, time.Hour} {
		q, err := BuildRequestQueries(step, SelectPods("shop", []string{"api-0"}), WorkloadMetricsScope{SingleCluster: true}, RequestSourceBeyla, "")
		if err != nil {
			t.Fatal(err)
		}
		window := WorkloadRateWindow(step)
		if window < 2*step || window < 5*time.Minute {
			t.Fatal("rate window leaves evaluation gaps")
		}
		for _, query := range []string{q.Rate, q.Errors, q.P50, q.P95, q.HistogramCoverage, q.StatusCoverage, q.ObservedPods} {
			if !strings.Contains(query, "["+window.String()+"]") {
				t.Fatalf("inconsistent window: %s", query)
			}
		}
	}
}

func TestRequestQueriesRejectAmbiguousScopeAndJobInjection(t *testing.T) {
	sel := SelectPods("shop", []string{"api-0"})
	for _, job := range []string{`job=~".*"} or up{job="x"`, `job!=""`, `job=~"["`, `job="a",namespace=~".*"`} {
		if _, err := BuildRequestQueries(time.Minute, sel, WorkloadMetricsScope{SingleCluster: true}, RequestSourceBeyla, job); err == nil {
			t.Errorf("accepted job fragment %q", job)
		}
	}
	if _, err := BuildRequestQueries(time.Minute, sel, WorkloadMetricsScope{}, RequestSourceBeyla, ""); err == nil {
		t.Fatal("unscoped shared store accepted")
	}
	if _, err := BuildRequestQueries(time.Minute, SelectPods("shop", nil), WorkloadMetricsScope{SingleCluster: true}, RequestSourceIstio, ""); err == nil {
		t.Fatal("empty pod scope accepted")
	}
}
