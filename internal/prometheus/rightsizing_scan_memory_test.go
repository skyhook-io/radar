package prometheus

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

func vmMemoryRefusal(requested int) *prom.HTTPError {
	body := fmt.Sprintf(`{"error":"not enough memory for processing metric: 100 time series; total available memory for concurrent requests: 100 bytes; requested memory: %d bytes"}`, requested)
	return &prom.HTTPError{StatusCode: 422, Body: []byte(body)}
}

func TestScanMemoryRecoveryKeepsSuccessfulFamilies(t *testing.T) {
	refusal := vmMemoryRefusal(75)
	client := &fakeScanQuerier{}
	cpuCalls := 0
	client.rangeFn = func(q string) (*prom.QueryResult, error) {
		if strings.Contains(q, metricCPUUsage+"{") {
			cpuCalls++
			if cpuCalls == 1 {
				return nil, refusal
			}
		}
		return &prom.QueryResult{Series: []prom.Series{scanMatrixSeries("shop", "Deployment", "api", "api", []float64{1})}}, nil
	}
	batch := []scanWorkload{scanTestWorkload("Deployment", "shop", "api", "api")}
	out := queryRightsizingScanBatch(context.Background(), client, batch, time.Unix(1800000000, 0), ViewScope{}, new(scanMemoryBudget))
	if len(out.errors) != 0 || cpuCalls != 2 || len(client.rangeQueries) != 4 || len(client.queries) != 2 || len(out.cpu) != 1 || len(out.memory) != 1 {
		t.Fatalf("unexpected recovery: cpu=%d range=%d instant=%d errors=%v", cpuCalls, len(client.rangeQueries), len(client.queries), out.errors)
	}
	recovered := 0
	for _, query := range append(slices.Clone(client.rangeQueries), client.queries...) {
		if strings.Contains(query, `container=~"^(api)$"`) {
			recovered++
		}
	}
	if recovered != 1 {
		t.Errorf("expected only the retried query to narrow containers, got %d", recovered)
	}
}

func TestScanMemoryRecoveryPreservesFullGrid(t *testing.T) {
	batch := []scanWorkload{scanTestWorkload("Deployment", "shop", "api", "api")}
	start := time.Unix(1800000000, 0)
	end := start.Add(rightsizingWindow)
	for _, failSecond := range []bool{false, true} {
		client := &fakeScanQuerier{}
		client.rangeFn = func(string) (*prom.QueryResult, error) {
			if failSecond && len(client.rangeQueries) == 2 {
				return nil, prom.ErrQueryWarning
			}
			return &prom.QueryResult{Series: []prom.Series{scanMatrixSeries("shop", "Deployment", "api", "api", []float64{1})}}, nil
		}
		result, err := new(scanMemoryBudget).recover(context.Background(), client, batch, ViewScope{}, "cpu", start, end, vmMemoryRefusal(185))
		if len(client.rangeQueries) != 2 || !client.rangeStarts[0].Equal(start) || !client.rangeEnds[1].Equal(end) || !client.rangeEnds[0].Add(rightsizingStep).Equal(client.rangeStarts[1]) {
			t.Fatalf("overlapping or missing range: starts=%v ends=%v", client.rangeStarts, client.rangeEnds)
		}
		if failSecond {
			if !errors.Is(err, prom.ErrQueryWarning) || result != nil {
				t.Fatalf("incomplete history escaped: %+v %v", result, err)
			}
		} else if err != nil || result == nil || len(result.Series) != 2 {
			t.Fatalf("missing segment: %+v %v", result, err)
		}
	}
}

func TestScanMemoryRecoveryPreservesSafetyWindowAndBoundsRetries(t *testing.T) {
	batch := []scanWorkload{scanTestWorkload("Deployment", "a", "api", "api"), scanTestWorkload("Deployment", "b", "api", "api")}
	now := time.Unix(1800000000, 0)
	client := &fakeScanQuerier{}
	_, err := new(scanMemoryBudget).recover(context.Background(), client, batch, ViewScope{}, "restart_activity", now.Add(-rightsizingWindow), now, vmMemoryRefusal(150))
	if err != nil || len(client.queries) != 2 || len(client.rangeQueries) != 0 {
		t.Fatalf("instant queries: %v; error %v", client.queries, err)
	}
	for i, ns := range []string{"a", "b"} {
		if !strings.Contains(client.queries[i], `namespace=~"^(`+ns+`)$"`) || !strings.Contains(client.queries[i], "[7d:5m]") {
			t.Errorf("incomplete safety subquery: %s", client.queries[i])
		}
	}
	nearDeadline, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, tc := range []struct {
		ctx   context.Context
		group []scanWorkload
		err   error
	}{
		{nearDeadline, batch, vmMemoryRefusal(75)},
		{context.Background(), batch, vmMemoryRefusal(401)},
		{context.Background(), batch, &prom.HTTPError{StatusCode: 422, Body: []byte(`{"error":"invalid expression"}`)}},
	} {
		before := len(client.queries)
		res, err := new(scanMemoryBudget).recover(tc.ctx, client, tc.group, ViewScope{}, "restart_activity", now.Add(-rightsizingWindow), now, tc.err)
		if res != nil || err != tc.err || len(client.queries) != before {
			t.Fatalf("unexpected retry: queries %d→%d, result %+v, error %v", before, len(client.queries), res, err)
		}
	}
}

func TestScanMemoryRecoveryPlansPhysicalQueriesBeforeRetrying(t *testing.T) {
	batch := []scanWorkload{scanTestWorkload("Deployment", "a", "api", "api"), scanTestWorkload("Deployment", "b", "api", "api")}
	scope := ViewScope{Filters: map[string]string{metricCPUUsage: `cluster=""`}}
	base := buildScanQueries(batch, scope, true)["cpu"]
	scope.Filters[metricCPUUsage] = `cluster="` + strings.Repeat("x", prom.MaxWorkloadQueryBytes-len(base)+1) + `"`
	if len(buildScanQueries(batch[:1], scope, true)["cpu"]) > prom.MaxWorkloadQueryBytes {
		t.Fatal("one workload should fit")
	}
	now := time.Unix(1800000000, 0)
	for _, tc := range []struct {
		budget    scanMemoryBudget
		requested int
	}{
		{scanMemoryBudget{}, 250}, // Three time ranges each require two byte-limited requests.
		{scanMemoryBudget{spent: rightsizingScanQueryTimeout}, 75},
	} {
		client := &fakeScanQuerier{}
		refusal := vmMemoryRefusal(tc.requested)
		result, err := tc.budget.recover(context.Background(), client, batch, scope, "cpu", now.Add(-rightsizingWindow), now, refusal)
		if result != nil || err != refusal || len(client.rangeQueries) != 0 {
			t.Fatalf("unexpected recovery traffic: result %+v error %v requests %d", result, err, len(client.rangeQueries))
		}
	}
}

func TestScanMemoryRecoveryPartitionsInstantFamilies(t *testing.T) {
	now := time.Unix(1800000000, 0)
	for _, namespaces := range [][]string{{"a", "b", "c", "d", "e"}, {"a", "a", "a", "a", "a"}} {
		var batch []scanWorkload
		for i, ns := range namespaces {
			name := fmt.Sprintf("worker-%d", i)
			batch = append(batch, scanTestWorkload("Deployment", ns, name, name))
		}
		client := &fakeScanQuerier{}
		_, err := new(scanMemoryBudget).recover(context.Background(), client, batch, ViewScope{}, "termination_history", now.Add(-rightsizingWindow), now, vmMemoryRefusal(320))
		if err != nil || len(client.queries) != 4 {
			t.Fatalf("expected four instant queries, got %d, error %v", len(client.queries), err)
		}
		for _, q := range client.queries {
			if !strings.Contains(q, "[7d:5m]") {
				t.Fatalf("lost safety history window in query %q", q)
			}
		}
		for _, w := range batch {
			matches := 0
			for _, q := range client.queries {
				if strings.Contains(q, w.name) {
					matches++
				}
			}
			if matches != 1 {
				t.Fatalf("workload %q occurs in %d partitions", w.name, matches)
			}
		}
	}
}
