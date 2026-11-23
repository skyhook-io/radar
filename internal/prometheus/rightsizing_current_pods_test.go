package prometheus

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
	"github.com/skyhook-io/radar/pkg/rightsizing"
	corev1 "k8s.io/api/core/v1"
)

func currentSizingWorkload() scanWorkload {
	w := scanTestWorkload("Deployment", "apps", "api", "app")
	w.replicas = 2
	w.workload.podIdentities = []prom.WorkloadPodIdentity{
		{Name: "api-a", UID: "aaaaaaaa-1111-2222-3333-444444444444"},
		{Name: "api-b", UID: "bbbbbbbb-1111-2222-3333-444444444444"},
	}
	w.workload.qos.Observe(corev1.PodQOSBurstable)
	w.workload.podRestartEvidence = map[string]sizingPodRestarts{"app": {knownPods: 2}}
	return w
}

func currentSizingSeries(p prom.WorkloadPodIdentity, start time.Time, points int, v float64) prom.Series {
	out := prom.Series{Labels: map[string]string{"pod": p.Name, "namespace": "apps", "container": "app", "id": "/kubepods/pod" + p.UID + "/container"}}
	for i := 0; i < points; i++ {
		out.DataPoints = append(out.DataPoints, prom.DataPoint{Timestamp: start.Add(time.Duration(i) * rightsizingStep).Unix(), Value: v})
	}
	return out
}

func TestCurrentPodRightsizingRequiresUniqueCompleteBuckets(t *testing.T) {
	w := currentSizingWorkload()
	a, b := w.workload.podIdentities[0], w.workload.podIdentities[1]
	now := time.Unix(60000000, 0)
	start := now.Add(-rightsizingWindow)
	client := &fakeScanQuerier{rangeFn: func(q string) (*prom.QueryResult, error) {
		if !strings.HasPrefix(q, "max by (namespace,pod,container,id) (") || !strings.Contains(q, a.UID) || !strings.Contains(q, b.UID) || !strings.Contains(q, "id=~") || strings.Contains(q, "kube_pod_owner") {
			t.Errorf("query missing UID constraint: %s", q)
		}
		if strings.Contains(q, metricCPUUsage) && !strings.Contains(q, `cpu=~"total|"`) {
			t.Errorf("CPU query admits partial per-core series: %s", q)
		}
		both := []prom.Series{currentSizingSeries(a, start, 4, .05), currentSizingSeries(b, start.Add(rightsizingStep), 3, .2)}
		// Same-named foreign Pod, another namespace, duplicate scrape, old
		// UID on a new Pod name, and an off-grid sample must add no evidence.
		foreign := currentSizingSeries(prom.WorkloadPodIdentity{Name: a.Name, UID: "cccccccc-1111-2222-3333-444444444444"}, start, 4, .8)
		wrongNS := currentSizingSeries(a, start, 4, .9)
		wrongNS.Labels["namespace"] = "other"
		wrongName := currentSizingSeries(a, start, 4, .9)
		wrongName.Labels["pod"] = b.Name
		dupe := currentSizingSeries(b, start.Add(rightsizingStep), 1, .2)
		dupe.Labels["job"] = "replica"
		both = append(both, foreign, wrongNS, wrongName, dupe, currentSizingSeries(a, start.Add(time.Second), 1, .99))
		return &prom.QueryResult{ResultType: "matrix", Series: both}, nil
	}}
	e := queryCurrentPodRightsizing(context.Background(), client, w, now)
	key := scanKey{"apps", "Deployment", "api", "app"}
	if !slices.Equal(e.cpu[key], []float64{.2, .2}) || !slices.Equal(e.throttle[key], []float64{.2, .2}) || len(e.memory[key]) != 2 {
		t.Fatalf("expected two complete nonduplicated buckets, got %+v", e)
	}
	if len(client.queries) != 0 || len(client.rangeQueries) != 3 {
		t.Fatalf("unexpected store reads: %+v", client)
	}
}

func TestCurrentPodScanClassifiesOnlyVerifiedCohorts(t *testing.T) {
	now := time.Unix(60000000, 0)
	w := currentSizingWorkload()
	incomplete := currentSizingWorkload()
	incomplete.name = "incomplete"
	incomplete.workload.podIdentityIncomplete = true
	client := &fakeScanQuerier{rangeFn: func(q string) (*prom.QueryResult, error) {
		v := .05
		if strings.Contains(q, metricMemoryWorkingSet) {
			v = 80 * 1024 * 1024
		}
		if strings.Contains(q, metricThrottledPeriods) {
			v = 0
		}
		var series []prom.Series
		for _, pod := range w.workload.podIdentities {
			series = append(series, currentSizingSeries(pod, now.Add(-rightsizingWindow), 500, v))
		}
		return &prom.QueryResult{ResultType: "matrix", Series: series}, nil
	}}
	resp := computeRightsizingScanProgress(sizingTunnelTestContext(context.Background()), client, func(context.Context, []scanWorkload) (ViewScope, error) {
		return ViewScope{State: ViewScopeUnlabeled}, nil
	}, []scanWorkload{incomplete, w}, newRightsizingScanResponse(now, RightsizingScanScope{}), func(RightsizingScanResponse) {})
	if resp.ClusterScope.State != ViewScopeCurrentPodUIDs || resp.Coverage.WorkloadsWithData != 1 || len(client.queries) != 0 {
		t.Fatalf("scope or coverage: %+v", resp)
	}
	for _, row := range resp.Workloads[0].Rows {
		if row.Confidence != ConfidenceMedium || row.Decision.Status != rightsizing.DecisionActionable || row.SampleCount != 500 {
			t.Fatalf("expected medium-confidence change: %+v", row)
		}
	}
	for _, row := range resp.Workloads[1].Rows {
		if row.Observed != nil || row.RecommendedReq != nil || row.Decision.Status != rightsizing.DecisionNone {
			t.Fatalf("incomplete cohort: %+v", row)
		}
	}
	w.workload.podRestartEvidence["app"] = sizingPodRestarts{knownPods: 1}
	rows := buildScanWorkload(w, queryCurrentPodRightsizing(context.Background(), client, w, now)).Rows
	if rows[1].OOMEvidenceAvailable || rows[1].Decision.Status == rightsizing.DecisionActionable {
		t.Fatalf("unverified restart history: %+v", rows[1])
	}
}

func TestCurrentPodSizingBatchesLargeCohortsAndRejectsPartialAnswers(t *testing.T) {
	w := currentSizingWorkload()
	for i := len(w.workload.podIdentities); i <= sizingPodQueryChunk; i++ {
		w.workload.podIdentities = append(w.workload.podIdentities, prom.WorkloadPodIdentity{Name: fmt.Sprintf("api-%d", i), UID: fmt.Sprintf("%08x-1111-2222-3333-444444444444", i)})
	}
	w.replicas = len(w.workload.podIdentities)
	client := &fakeScanQuerier{rangeFn: func(q string) (*prom.QueryResult, error) {
		if len(url.QueryEscape(q)) > sizingQueryEncodedBytes {
			t.Fatalf("unbounded query: %d", len(q))
		}
		return &prom.QueryResult{Annotations: prom.QueryAnnotations{Partial: strings.Contains(q, metricCPUUsage)}}, nil
	}}
	e := queryCurrentPodRightsizing(context.Background(), client, w, time.Unix(60000000, 0))
	if len(client.rangeQueries) != 5 || e.errors["cpu"] == nil {
		t.Fatalf("unexpected reads %d, errors %v", len(client.rangeQueries), e.errors)
	}
}

func TestCurrentPodDetailReportsCoverageAndQueryFailures(t *testing.T) {
	w := currentSizingWorkload()
	for _, tc := range []struct {
		name    string
		count   int
		partial bool
		want    rightsizing.DecisionStatus
	}{{"six hours cannot justify a reduction", 72, false, rightsizing.DecisionReview}, {"two days of complete replicas", 576, false, rightsizing.DecisionActionable}, {"store returned partial data", 576, true, rightsizing.DecisionNone}} {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeScanQuerier{rangeFn: func(q string) (*prom.QueryResult, error) {
				if tc.partial {
					return &prom.QueryResult{Annotations: prom.QueryAnnotations{Partial: true}}, nil
				}
				v := .05
				if strings.Contains(q, metricThrottledPeriods) {
					v = 0
				}
				if strings.Contains(q, metricMemoryWorkingSet) {
					v = 80 * 1024 * 1024
				}
				// Keep all samples strictly inside the requested week even if the
				// clock crosses a five-minute boundary during the test.
				start := time.Now().Truncate(rightsizingStep).Add(-6 * 24 * time.Hour)
				result := &prom.QueryResult{ResultType: "matrix"}
				for _, p := range w.workload.podIdentities {
					result.Series = append(result.Series, currentSizingSeries(p, start, tc.count, v))
				}
				return result, nil
			}}
			detail := currentPodRightsizingDetail(context.Background(), client, w)
			if detail.ClusterScope.State != ViewScopeCurrentPodUIDs || detail.OwnerCoverage != OwnerCoverageCurrentPods || detail.SampleAvailable == tc.partial || len(detail.Rows) != 2 {
				t.Fatalf("detail: %+v", detail)
			}
			for _, row := range detail.Rows {
				if row.Decision.Status != tc.want {
					t.Fatalf("%s: decision %+v, want %s", row.Resource, row.Decision, tc.want)
				}
				if !tc.partial && row.SampleCount != tc.count {
					t.Errorf("samples %d, want %d", row.SampleCount, tc.count)
				}
			}
			if tc.partial && (detail.Reason != ReasonRightsizingPartialData || len(detail.Warnings) != 3) {
				t.Fatalf("partial response missing explanation: %+v", detail)
			}
		})
	}
}

func TestFleetAttributionPreservesStandaloneAndVerifiedHistory(t *testing.T) {
	now := time.Unix(60000000, 0)
	w := currentSizingWorkload()
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		state ViewScopeState
	}{
		{"standalone", context.Background(), ViewScopeUnlabeled},
		{"verified fleet history", sizingTunnelTestContext(context.Background()), ViewScopeVerified},
		{"configured fleet history", sizingTunnelTestContext(context.Background()), ViewScopeConfigured},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeScanQuerier{
				queryFn: func(string) (*prom.QueryResult, error) { return ksmAvailable(), nil },
				rangeFn: func(q string) (*prom.QueryResult, error) {
					if strings.Contains(q, "id=~") || !strings.Contains(q, metricPodOwner) {
						t.Errorf("expected retained workload history: %s", q)
					}
					s := scanMatrixSeries(w.namespace, w.kind, w.name, "app", repeatedValues(.05))
					return &prom.QueryResult{ResultType: "matrix", Series: []prom.Series{s}}, nil
				},
			}
			scope := ViewScope{State: tc.state}
			resp := computeRightsizingScanProgress(tc.ctx, client, func(context.Context, []scanWorkload) (ViewScope, error) { return scope, nil }, []scanWorkload{w}, newRightsizingScanResponse(now, RightsizingScanScope{}), func(RightsizingScanResponse) {})
			if resp.ClusterScope.State != tc.state || resp.Coverage.WorkloadsWithData != 1 {
				t.Fatalf("existing scope or data lost: %+v", resp)
			}
		})
	}
}

func TestCurrentPodRowReasons(t *testing.T) {
	input := currentSizingWorkload()
	key := scanKey{input.namespace, input.kind, input.name, input.workload.containers[0].name}
	evidence := scanBatchEvidence{ownerCoverage: OwnerCoverageCurrentPods, cpu: map[scanKey][]float64{key: {.05}}}
	rows := buildScanWorkload(input, evidence).Rows
	if rows[0].RecommendationReason != "insufficient_history" || rows[1].RecommendationReason != "current_pod_samples_missing" {
		t.Fatalf("short CPU history and missing memory samples should be distinct: %+v", rows)
	}
	input.workload.podIdentityIncomplete = true
	evidence.cpu = nil
	rows = buildScanWorkload(input, evidence).Rows
	for _, row := range rows {
		if row.RecommendationReason != "current_replicas_unverified" {
			t.Fatalf("missing Pod verification reason: %+v", row)
		}
	}
}

func TestTunnelScanWithNoReadyCohortReportsWhyWithoutReadingUnattributedHistory(t *testing.T) {
	w := currentSizingWorkload()
	w.workload.podIdentityIncomplete = true
	client := &fakeScanQuerier{}
	ctx := sizingTunnelTestContext(context.Background())
	resp := computeRightsizingScanProgress(ctx, client, func(context.Context, []scanWorkload) (ViewScope, error) {
		return ViewScope{State: ViewScopeUnlabeled}, nil
	}, []scanWorkload{w}, newRightsizingScanResponse(time.Now(), RightsizingScanScope{}), func(RightsizingScanResponse) {})
	if resp.ClusterScope.State != ViewScopeCurrentPodUIDs || resp.State != RightsizingScanPartial || len(resp.Warnings) == 0 || resp.Warnings[0].Code != "current_replicas_unverified" {
		t.Fatalf("missing reason for withheld measurements: %+v", resp)
	}
	detail := currentPodRightsizingDetail(ctx, client, w)
	if detail.SampleAvailable || !strings.Contains(detail.Reason, "ready Pods") || len(client.queries)+len(client.rangeQueries) != 0 {
		t.Fatalf("unexpected detail or store reads: %+v, %+v", detail, client)
	}
}

func TestCurrentPodRangeSplitsOversizedReplyWithoutLosingReplicas(t *testing.T) {
	w := currentSizingWorkload()
	now := time.Unix(60000000, 0)
	a, b := w.workload.podIdentities[0], w.workload.podIdentities[1]
	store := &fakeScanQuerier{rangeFn: func(q string) (*prom.QueryResult, error) {
		if strings.Contains(q, a.UID) && strings.Contains(q, b.UID) {
			return nil, fmt.Errorf("transport bound: %w", prom.ErrResponseTooLarge)
		}
		p, v := a, .1
		if strings.Contains(q, b.UID) {
			p, v = b, .2
		}
		return &prom.QueryResult{ResultType: "matrix", Series: []prom.Series{currentSizingSeries(p, now, 1, v)}}, nil
	}}
	e := queryCurrentPodRightsizing(context.Background(), store, w, now)
	key := scanKey{w.namespace, w.kind, w.name, "app"}
	if len(e.errors) != 0 || !slices.Equal(e.cpu[key], []float64{.2}) || len(store.rangeQueries) != 9 {
		t.Fatalf("split reads %d: %+v", len(store.rangeQueries), e)
	}
}

func TestCurrentPodRangeBudgetsLongNamespaceQueriesAndWidePods(t *testing.T) {
	var targets []sizingPodTarget
	for i := 0; i < sizingPodQueryChunk; i++ {
		w := currentSizingWorkload()
		w.namespace = strings.Repeat("n", 60) + fmt.Sprintf("%03d", i)
		pod := prom.WorkloadPodIdentity{Name: fmt.Sprintf("pod-%d", i), UID: fmt.Sprintf("%08x-1111-2222-3333-444444444444", i)}
		targets = append(targets, sizingPodTarget{identity: pod, workload: &w})
	}
	if len(url.QueryEscape(sizingPodRangeQuery("throttle", targets))) <= sizingQueryEncodedBytes {
		t.Fatal("fixture must exceed the encoded request budget")
	}
	client := &fakeScanQuerier{rangeFn: func(q string) (*prom.QueryResult, error) {
		if len(url.QueryEscape(q)) > sizingQueryEncodedBytes {
			t.Errorf("encoded request size %d", len(url.QueryEscape(q)))
		}
		return &prom.QueryResult{ResultType: "matrix"}, nil
	}}
	ctx := context.Background()
	now := time.Unix(60000000, 0)
	if _, err := currentPodRangeValues(ctx, client, "throttle", targets, nil, now.Add(-rightsizingWindow), now); err != nil {
		t.Fatal(err)
	}
	if len(client.rangeQueries) < 2 {
		t.Fatal("oversized URI was not split")
	}
	for _, tgt := range targets {
		tgt.workload.workload.containers = make([]containerSpec, 20)
	}
	client.rangeQueries = nil
	if _, err := currentPodRangeValues(ctx, client, "memory", targets, nil, now.Add(-rightsizingWindow), now); err != nil {
		t.Fatal(err)
	}
	if len(client.rangeQueries) != (len(targets)+2)/3 {
		t.Fatalf("expected <=3 wide Pods per range, got %d requests", len(client.rangeQueries))
	}
}
