package prometheus

import (
	"context"
	"fmt"
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
	w.workload.podZeroRestarts = map[string]int{"app": 2}
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
		if !strings.Contains(q, a.UID) || !strings.Contains(q, b.UID) || !strings.Contains(q, "id=~") || strings.Contains(q, "kube_pod_owner") {
			t.Errorf("query missing UID constraint: %s", q)
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
		if strings.HasPrefix(q, metricMemoryWorkingSet) {
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
	w.workload.podZeroRestarts["app"] = 1
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
		if len(q) > prom.MaxWorkloadQueryBytes {
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
				if strings.HasPrefix(q, metricMemoryWorkingSet) {
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
