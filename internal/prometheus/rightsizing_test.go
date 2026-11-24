package prometheus

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
	"github.com/skyhook-io/radar/pkg/rightsizing"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type fakeRightsizingQuerier func(string) (*prom.QueryResult, error)

func (f fakeRightsizingQuerier) Query(_ context.Context, query string) (*prom.QueryResult, error) {
	return f(query)
}

func containerResult(values map[string]float64) *prom.QueryResult {
	result := &prom.QueryResult{ResultType: "vector"}
	for container, value := range values {
		result.Series = append(result.Series, prom.Series{
			Labels:     map[string]string{"container": container},
			DataPoints: []prom.DataPoint{{Value: value}},
		})
	}
	return result
}

func terminationResult(containerReasons map[string][]string) *prom.QueryResult {
	result := &prom.QueryResult{ResultType: "vector"}
	for container, reasons := range containerReasons {
		for _, reason := range reasons {
			result.Series = append(result.Series, prom.Series{
				Labels:     map[string]string{"container": container, "reason": reason},
				DataPoints: []prom.DataPoint{{Value: 1}},
			})
		}
	}
	return result
}

func mustQuantity(t *testing.T, s string) *resource.Quantity {
	t.Helper()
	q := resource.MustParse(s)
	return &q
}

func TestClassifyRightsizingFit(t *testing.T) {
	q := func(s string) *resource.Quantity { return mustQuantity(t, s) }

	tests := []struct {
		name                       string
		observed                   float64
		req, lim                   *resource.Quantity
		resource                   string
		hpa, oom                   bool
		wantFit                    RightsizingFit
		wantRec, wantLimitConflict bool
		wantReason                 string
	}{
		{"balanced inside 30 percent band", 0.08, q("100m"), q("1"), "cpu", false, false, FitBalanced, false, false, "request_within_fit_range"},
		{"oversized beyond 30 percent reduction", 0.05, q("100m"), q("1"), "cpu", false, false, FitOversized, true, false, ""},
		{"under requested includes headroom", 0.09, q("100m"), q("1"), "cpu", false, false, FitUnderRequested, true, false, ""},
		{"missing request", 0.2, nil, q("1"), "cpu", false, false, FitMissingRequest, true, false, ""},
		{"zero request is missing", 0.2, q("0"), q("1"), "cpu", false, false, FitMissingRequest, true, false, ""},
		{"HPA suppresses only its resource", 0.05, q("200m"), q("1"), "cpu", true, false, FitOversized, false, false, "hpa_managed"},
		{"memory OOM suppresses shrink", 50 * 1024 * 1024, q("256Mi"), q("1Gi"), "memory", false, true, FitOversized, false, false, "oom_evidence"},
		{"memory OOM permits increase", 300 * 1024 * 1024, q("128Mi"), q("1Gi"), "memory", false, true, FitUnderRequested, true, false, ""},
		{"recommended request above limit is withheld", 0.95, q("100m"), q("1"), "cpu", false, false, FitUnderRequested, false, true, "recommended_request_exceeds_limit"},
		{"rounded CPU request above limit is withheld", 0.095, q("50m"), q("105m"), "cpu", false, false, FitUnderRequested, false, true, "recommended_request_exceeds_limit"},
		{"rounded memory request above limit is withheld", 100 * 1024 * 1024, q("64Mi"), q("120Mi"), "memory", false, false, FitUnderRequested, false, true, "recommended_request_exceeds_limit"},
		{"rounded target equal to request is in range", 0, q("10m"), nil, "cpu", false, false, FitBalanced, false, false, "request_within_fit_range"},
		{"rounding does not turn covered demand into an increase", 0.9, q("1200m"), q("2"), "cpu", false, false, FitBalanced, false, false, "request_within_fit_range"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := RightsizingRow{HPAManaged: tc.hpa, HPAEvidenceAvailable: true, CurrentPodOOM: tc.oom, OOMEvidenceAvailable: true}
			rightsizing.ClassifyRightsizingFit(&row, tc.observed, tc.req, tc.lim, tc.resource)
			if row.Fit != tc.wantFit {
				t.Errorf("fit = %s, want %s", row.Fit, tc.wantFit)
			}
			if row.RecommendationReason != tc.wantReason {
				t.Errorf("reason = %q, want %q", row.RecommendationReason, tc.wantReason)
			}
			if tc.wantRec && row.RecommendedReq == nil {
				t.Errorf("expected RecommendedReq populated, got nil")
			}
			if !tc.wantRec && row.RecommendedReq != nil {
				t.Errorf("expected no RecommendedReq, got %q", *row.RecommendedReq)
			}
			if row.LimitConflict != tc.wantLimitConflict {
				t.Errorf("limitConflict = %t, want %t", row.LimitConflict, tc.wantLimitConflict)
			}
		})
	}
}

func TestIsBurstyCPURequiresMaterialAbsoluteAndRelativeGap(t *testing.T) {
	if !rightsizing.IsBurstyCPU(0.02, 0.20) {
		t.Fatal("expected a material P95-to-P99 jump to be bursty")
	}
	if rightsizing.IsBurstyCPU(0.10, 0.20) {
		t.Fatal("a two-fold jump must not be marked bursty")
	}
	if rightsizing.IsBurstyCPU(0.001, 0.010) {
		t.Fatal("a tiny absolute jump must not be marked bursty")
	}
}

func TestComputeRightsizingUsesGroupedEvidence(t *testing.T) {
	workload := rightsizingWorkload{
		containers: []containerSpec{{
			name: "server", cpuReq: mustQuantity(t, "500m"), cpuLim: mustQuantity(t, "1"),
			memReq: mustQuantity(t, "512Mi"), memLim: mustQuantity(t, "1Gi"),
		}},
		currentPodOOM: map[string]bool{},
		hpaManaged:    map[string]bool{},
		hpaAvailable:  true,
	}
	querier := fakeRightsizingQuerier(func(query string) (*prom.QueryResult, error) {
		switch {
		case strings.HasPrefix(query, "sum(count_over_time"):
			return &prom.QueryResult{Series: []prom.Series{{DataPoints: []prom.DataPoint{{Value: 1}}}}}, nil
		case strings.HasPrefix(query, "quantile_over_time(0.95"):
			return containerResult(map[string]float64{"server": 0.1}), nil
		case strings.HasPrefix(query, "quantile_over_time(0.99") && strings.Contains(query, "container_cpu_usage_seconds_total"):
			return containerResult(map[string]float64{"server": 0.4}), nil
		case strings.HasPrefix(query, "count_over_time"):
			return containerResult(map[string]float64{"server": 2016}), nil
		case strings.HasPrefix(query, "max_over_time") && strings.Contains(query, "container_memory_working_set_bytes"):
			return containerResult(map[string]float64{"server": 100 * 1024 * 1024}), nil
		case strings.HasPrefix(query, "max_over_time") && strings.Contains(query, "container_cpu_cfs_throttled"):
			return containerResult(map[string]float64{"server": 0.2}), nil
		case strings.Contains(query, "kube_pod_container_status_restarts_total"):
			return containerResult(map[string]float64{"server": 0}), nil
		case strings.Contains(query, "last_terminated_timestamp"):
			return terminationResult(nil), nil
		default:
			return nil, errors.New("unexpected query")
		}
	})

	response := computeRightsizing(context.Background(), querier, "Deployment", "argocd", "argocd-server", workload, ViewScope{})
	if response.OwnerCoverage != OwnerCoverageKSMHistory || !response.SampleAvailable {
		t.Fatalf("unexpected response coverage/availability: %+v", response)
	}
	if len(response.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(response.Rows))
	}
	cpu := response.Rows[0]
	if cpu.Fit != FitOversized || cpu.Confidence != ConfidenceHigh || cpu.RecommendedReq == nil {
		t.Errorf("CPU row = %+v", cpu)
	}
	if cpu.ThrottleRatio == nil || *cpu.ThrottleRatio != 0.2 {
		t.Errorf("throttle evidence not preserved: row=%+v", cpu)
	}
	memory := response.Rows[1]
	if memory.Observed == nil || memory.Observed.Name != "Max" || memory.Fit != FitOversized || !memory.OOMEvidenceAvailable || memory.RecommendedReq == nil {
		t.Errorf("memory row = %+v", memory)
	}
}

func TestMemoryReductionRequiresVerifiedRestartHistory(t *testing.T) {
	tests := []struct {
		name               string
		restarts           queryOutcome
		terminations       queryOutcome
		wantAvailable      bool
		wantWindowOOM      bool
		wantReason         string
		wantRecommendation bool
	}{
		{
			name:          "clean window",
			restarts:      queryOutcome{values: map[string]float64{"server": 0}},
			terminations:  queryOutcome{terminations: map[string]terminationEvidence{}},
			wantAvailable: true, wantRecommendation: true,
		},
		{
			name:          "non OOM restart with reason",
			restarts:      queryOutcome{values: map[string]float64{"server": 1}},
			terminations:  queryOutcome{terminations: map[string]terminationEvidence{"server": {Any: true}}},
			wantAvailable: true, wantRecommendation: true,
		},
		{
			name:         "restart without termination reason",
			restarts:     queryOutcome{values: map[string]float64{"server": 1}},
			terminations: queryOutcome{terminations: map[string]terminationEvidence{}},
			wantReason:   "oom_evidence_unavailable",
		},
		{
			name:          "OOM followed by another termination",
			restarts:      queryOutcome{values: map[string]float64{"server": 2}},
			terminations:  queryOutcome{terminations: map[string]terminationEvidence{"server": {Any: true, OOM: true}}},
			wantAvailable: true, wantWindowOOM: true, wantReason: "oom_evidence",
		},
		{
			name:         "restart query failure",
			restarts:     queryOutcome{err: errors.New("metric unavailable")},
			terminations: queryOutcome{terminations: map[string]terminationEvidence{}},
			wantReason:   "oom_evidence_unavailable",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			results := map[string]queryOutcome{
				"memory_stat":         {values: map[string]float64{"server": 50 * 1024 * 1024}},
				"memory_coverage":     {values: map[string]float64{"server": 2016}},
				"restart_activity":    tc.restarts,
				"termination_history": tc.terminations,
			}
			workload := rightsizingWorkload{hpaAvailable: true, currentPodOOM: map[string]bool{}, hpaManaged: map[string]bool{}}
			container := containerSpec{name: "server", memReq: mustQuantity(t, "256Mi")}
			row := buildRightsizingRow(container, "memory", 2016, OwnerCoverageKSMHistory, workload, results)
			if row.OOMEvidenceAvailable != tc.wantAvailable || row.WindowOOMEvidence != tc.wantWindowOOM || row.RecommendationReason != tc.wantReason || (row.RecommendedReq != nil) != tc.wantRecommendation {
				t.Fatalf("row = %+v", row)
			}
		})
	}
}

func TestTerminationEvidenceKeepsHistoricalOOMReason(t *testing.T) {
	evidence := terminationEvidenceByContainer(terminationResult(map[string][]string{"server": {"Error", "OOMKilled"}}))
	if !evidence["server"].Any || !evidence["server"].OOM {
		t.Fatalf("termination evidence = %+v", evidence)
	}
}

func TestWorkloadSelectionFallsBackToExactCurrentPods(t *testing.T) {
	var mu sync.Mutex
	var queries []string
	querier := fakeRightsizingQuerier(func(query string) (*prom.QueryResult, error) {
		mu.Lock()
		queries = append(queries, query)
		mu.Unlock()
		return &prom.QueryResult{}, nil
	})
	selection, coverage, _ := workloadSelection(context.Background(), querier, "Deployment", "prod", "api", []string{"api-6ccf7b8d9-x1"}, ViewScope{})
	if coverage != OwnerCoverageCurrentPods {
		t.Fatalf("coverage = %q, want current_pods", coverage)
	}
	if selection.podPattern != `^(api-6ccf7b8d9-x1)$` {
		t.Errorf("selection is not exact: %+v", selection)
	}
	if strings.Contains(selection.podPattern, "api-worker") || strings.Contains(selection.podPattern, `api-.*`) {
		t.Errorf("selection can collide with sibling workloads: %+v", selection)
	}
	if got := rightsizing.ConfidenceFor(2016, 1, coverage); got != ConfidenceMedium {
		t.Errorf("current-pod confidence = %q, want medium", got)
	}
	if got := rightsizing.ConfidenceFor(72, 72.0/2016.0, coverage); got != ConfidenceLow {
		t.Errorf("sparse current-pod confidence = %q, want low", got)
	}
	for key, query := range buildRightsizingQueries("prod", selection) {
		if !strings.Contains(query, `pod=~"^(api-6ccf7b8d9-x1)$"`) {
			t.Errorf("%s does not apply exact pods directly to its metric selector: %s", key, query)
		}
		if strings.Contains(query, `and on (namespace,pod) {`) {
			t.Errorf("%s uses an unbounded bare selector: %s", key, query)
		}
	}
	if len(queries) != 1 || !strings.Contains(queries[0], `owner_name="api"`) {
		t.Errorf("KSM ownership probe must use exact owner identity: %v", queries)
	}
}

func TestAuxiliaryQueryFailureDoesNotMaskMissingUsageSamples(t *testing.T) {
	workload := rightsizingWorkload{
		containers:    []containerSpec{{name: "server"}},
		podNames:      []string{"api-6ccf7b8d9-x1"},
		currentPodOOM: map[string]bool{},
		hpaManaged:    map[string]bool{},
	}
	querier := fakeRightsizingQuerier(func(query string) (*prom.QueryResult, error) {
		if strings.Contains(query, "container_cpu_cfs_throttled") {
			return nil, errors.New("throttle metrics unavailable")
		}
		return &prom.QueryResult{}, nil
	})

	response := computeRightsizing(context.Background(), querier, "Deployment", "prod", "api", workload, ViewScope{})
	if response.Reason != "No workload usage samples are available in the last 7d." {
		t.Errorf("reason = %q, want missing usage samples", response.Reason)
	}
}

func TestOwnerSelectionDeduplicatesKSMTargets(t *testing.T) {
	query := ownerSelection("Deployment", "prod", "api", ViewScope{})
	for _, want := range []string{
		"max by (namespace,pod,owner_name)",
		"max by (namespace,replicaset)",
		`owner_name="api"`,
	} {
		if !strings.Contains(query, want) {
			t.Errorf("owner query missing %q: %s", want, query)
		}
	}
}

func TestExtractRuntimeContainers(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	onFailure := corev1.ContainerRestartPolicy("OnFailure")

	tests := []struct {
		name      string
		spec      *corev1.PodSpec
		wantNames []string
	}{
		{"regular containers only", &corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app"}, {Name: "proxy"}},
		}, []string{"app", "proxy"}},

		{"pure init excluded", &corev1.PodSpec{
			Containers:     []corev1.Container{{Name: "app"}},
			InitContainers: []corev1.Container{{Name: "migrate"}},
		}, []string{"app"}},

		// Load-bearing native-sidecar behavior — without this the request/limit
		// overlay misses the sidecar's contribution.
		{"native sidecar included", &corev1.PodSpec{
			Containers:     []corev1.Container{{Name: "app"}},
			InitContainers: []corev1.Container{{Name: "envoy", RestartPolicy: &always}},
		}, []string{"app", "envoy"}},

		{"non-Always init excluded even with restart policy set", &corev1.PodSpec{
			Containers:     []corev1.Container{{Name: "app"}},
			InitContainers: []corev1.Container{{Name: "boot", RestartPolicy: &onFailure}},
		}, []string{"app"}},

		{"init-only pod returns empty runtime", &corev1.PodSpec{
			InitContainers: []corev1.Container{{Name: "job"}},
		}, []string{}},

		{"regular + sidecar + pure init mix", &corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app"}},
			InitContainers: []corev1.Container{
				{Name: "wait-db"},
				{Name: "envoy", RestartPolicy: &always},
			},
		}, []string{"app", "envoy"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractRuntimeContainers(tc.spec)
			gotNames := make([]string, len(got))
			for i, c := range got {
				gotNames[i] = c.name
			}
			if !slicesEqual(gotNames, tc.wantNames) {
				t.Errorf("names = %v, want %v", gotNames, tc.wantNames)
			}
		})
	}
}

func TestFormatObservedValueDoesNotRoundLikeARecommendation(t *testing.T) {
	if got := rightsizing.FormatObservedValue(0.0022, "cpu"); got != "2m" {
		t.Errorf("CPU observed = %q, want 2m", got)
	}
	if got := rightsizing.FormatObservedValue(39.4*1024*1024, "memory"); got != "39Mi" {
		t.Errorf("memory observed = %q, want 39Mi", got)
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestEvidenceQueryWarningsNameTheFailedSupportingQueries(t *testing.T) {
	warnings := evidenceQueryWarnings(map[string]queryOutcome{
		"cpu_stat":            {},
		"restart_activity":    {err: errors.New("context deadline exceeded")},
		"termination_history": {err: errors.New("context deadline exceeded")},
	})

	codes := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		codes = append(codes, warning.Code)
	}
	want := "restart_activity_query_failed,termination_history_query_failed"
	if got := strings.Join(codes, ","); got != want {
		t.Fatalf("a withheld memory recommendation must name the queries that failed behind it, got %q want %q", got, want)
	}
	// The codes are the ones a scan reports, so a caller comparing the two
	// scopes of the same workload reads one vocabulary.
	for _, warning := range warnings {
		if warning.Message == "" {
			t.Errorf("warning %q carries no message", warning.Code)
		}
	}
}

func TestEvidenceQueryWarningsStaySilentWhenEveryQueryAnswered(t *testing.T) {
	if warnings := evidenceQueryWarnings(map[string]queryOutcome{
		"cpu_stat":            {},
		"restart_activity":    {values: map[string]float64{"app": 0}},
		"termination_history": {terminations: map[string]terminationEvidence{}},
	}); len(warnings) != 0 {
		t.Fatalf("queries that answered are not a warning: %v", warnings)
	}
}

func TestEvidenceQueryWarningsCoverEveryQueryItRuns(t *testing.T) {
	// A query with no code is a failure that reaches no one: the row swallows
	// it and the envelope never mentions it.
	results := map[string]queryOutcome{}
	for key := range buildRightsizingQueries("dev", metricSelection{}) {
		results[key] = queryOutcome{err: errors.New("context deadline exceeded")}
	}
	if got, want := len(evidenceQueryWarnings(results)), len(results); got != want {
		t.Errorf("every query this builds must map to a warning code, got %d for %d queries", got, want)
	}
}

func TestCPUPeakFailureIsNotSilent(t *testing.T) {
	// Losing cpu_peak leaves Bursty false, which drops the manual-review flag
	// and loosens the clamp — so it must degrade the answer, unlike throttle.
	warnings := evidenceQueryWarnings(map[string]queryOutcome{
		"cpu_peak": {err: errors.New("context deadline exceeded")},
	})
	if len(warnings) != 1 || warnings[0].Code != "cpu_peak_query_failed" {
		t.Fatalf("a failed cpu_peak must be named, got %v", warnings)
	}
}

func measuredDecision(row RightsizingRow) RightsizingRow {
	row.Confidence = ConfidenceHigh
	row.HPAEvidenceAvailable = true
	row.Peak = &rightsizing.ObservedStatistic{Name: "P99"}
	var qos rightsizing.PodQoS
	qos.Observe(corev1.PodQOSBurstable)
	rows := []RightsizingRow{row}
	rightsizing.Finalize(rows, nil, qos, false)
	return rows[0]
}

func TestManualReviewReasonsNameEveryTrigger(t *testing.T) {
	reduction := func(row RightsizingRow) RightsizingRow {
		current, recommended := 0.5, 0.3
		row.CurrentRequestValue, row.RecommendedRequestValue = &current, &recommended
		return row
	}
	throttled := 0.307
	under := 0.05

	for _, tc := range []struct {
		name string
		row  RightsizingRow
		want string
	}{
		{"heavily throttled cut", reduction(RightsizingRow{ThrottleRatio: &throttled}), "throttled_reduction"},
		{"bursty cut", reduction(RightsizingRow{Bursty: true}), "bursty_reduction"},
		{"autoscaler owns it", RightsizingRow{HPAManaged: true}, "hpa_managed"},
		{"oom in window", RightsizingRow{WindowOOMEvidence: true}, "oom_in_window"},
		{"limit conflict", RightsizingRow{LimitConflict: true}, "limit_conflict"},
		{"withheld recommendation", RightsizingRow{RecommendationReason: ReasonOOMEvidenceUnavailable}, "recommendation_withheld,oom_evidence_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reasons := ManualReviewReasons(measuredDecision(tc.row))
			if strings.Join(reasons, ",") != tc.want {
				t.Errorf("got %v want %q", reasons, tc.want)
			}
			if !NeedsManualReview(measuredDecision(tc.row)) {
				t.Error("the verdict must agree with its own reasons")
			}
		})
	}

	// Light throttling on a cut, and heavy throttling on a row that is not a
	// cut, are both fine: the concern is a smaller request meeting a workload
	// that already wants more.
	if reasons := ManualReviewReasons(measuredDecision(reduction(RightsizingRow{ThrottleRatio: &under}))); len(reasons) != 0 {
		t.Errorf("throttling below the threshold is not a review trigger: %v", reasons)
	}
	if reasons := ManualReviewReasons(measuredDecision(RightsizingRow{ThrottleRatio: &throttled})); len(reasons) != 0 {
		t.Errorf("throttling without a reduction is not a review trigger: %v", reasons)
	}
}

func TestLostThrottleReadingClearsTheReviewReason(t *testing.T) {
	// The reason a failed throttle query degrades the answer: the same row
	// reads as a clean cut once the ratio is gone, and nothing on it says so.
	current, recommended := 0.5, 0.3
	throttled := 0.307
	row := RightsizingRow{CurrentRequestValue: &current, RecommendedRequestValue: &recommended, ThrottleRatio: &throttled}
	if !NeedsManualReview(measuredDecision(row)) {
		t.Fatal("a throttled cut needs review")
	}
	row.ThrottleRatio = nil
	if NeedsManualReview(measuredDecision(row)) {
		t.Error("without the ratio the row reads as a clean cut, which is why a failed throttle query has to reach the caller")
	}
}

func TestRecentPodRestartWithMissingHistory(t *testing.T) {
	end := time.Date(2026, 11, 24, 8, 12, 30, 0, time.UTC)
	birth := end.Add(-6 * 24 * time.Hour)
	key := scanKey{namespace: "lws-system", kind: "Deployment", workload: "lws-controller-manager", container: "manager"}
	container := containerSpec{name: key.container, memReq: mustQuantity(t, "256Mi")}
	clean := &corev1.ContainerStateTerminated{Reason: "Completed", ExitCode: 0}
	for _, tt := range []struct {
		name       string
		created    time.Time
		uid        types.UID
		restarts   int32
		last       *corev1.ContainerStateTerminated
		ksm        terminationEvidence
		wantReason string
	}{
		{"first sample already one", birth, "local", 1, nil, terminationEvidence{}, ReasonOOMEvidenceUnavailable},
		{"old Pod count alone cannot date its restart", end.Add(-8 * 24 * time.Hour), "local", 1, nil, terminationEvidence{}, ""},
		{"born during the latest partial five-minute bucket", end.Add(-time.Minute), "local", 1, nil, terminationEvidence{}, ReasonOOMEvidenceUnavailable},
		{"unknown runtime reason", birth, "local", 1, &corev1.ContainerStateTerminated{Reason: "ContainerStatusUnknown"}, terminationEvidence{}, ReasonOOMEvidenceUnavailable},
		{"generic SIGKILL", birth, "local", 1, &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 137}, terminationEvidence{}, ReasonOOMEvidenceUnavailable},
		{"older Pod with dated ambiguous exit", end.Add(-8 * 24 * time.Hour), "local", 1, &corev1.ContainerStateTerminated{Reason: "Error", Signal: 9, FinishedAt: metav1.NewTime(end.Add(-24 * time.Hour))}, terminationEvidence{}, ReasonOOMEvidenceUnavailable},
		{"older Pod with a dated known final exit", end.Add(-8 * 24 * time.Hour), "local", 2, &corev1.ContainerStateTerminated{Reason: "Completed", FinishedAt: metav1.NewTime(end.Add(-24 * time.Hour))}, terminationEvidence{}, ""},
		{"missing birth", time.Time{}, "local", 1, nil, terminationEvidence{}, ""},
		{"future birth", end.Add(time.Minute), "local", 1, nil, terminationEvidence{}, ""},
		{"unverified Pod", birth, "", 1, nil, terminationEvidence{}, ""},
		{"no restart", birth, "local", 0, nil, terminationEvidence{}, ""},
		{"one known clean exit", birth, "local", 1, clean, terminationEvidence{}, ""},
		{"two exits with one known reason", birth, "local", 2, clean, terminationEvidence{}, ReasonOOMEvidenceUnavailable},
		{"another replica can supply a non-OOM reason", birth, "local", 1, nil, terminationEvidence{Any: true}, ReasonOOMEvidenceUnavailable},
		{"positive OOM evidence wins", birth, "local", 1, nil, terminationEvidence{Any: true, OOM: true}, "oom_evidence"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := rightsizingWorkload{hpaAvailable: true, currentPodOOM: map[string]bool{}}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: tt.uid, CreationTimestamp: metav1.NewTime(tt.created)}}
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: key.container, RestartCount: tt.restarts, LastTerminationState: corev1.ContainerState{Terminated: tt.last}}}
			w.observePodTerminations(pod, end)
			const usage = 50 * 1024 * 1024
			detail := buildRightsizingRow(container, "memory", 2016, OwnerCoverageKSMHistory, w, map[string]queryOutcome{
				"memory_stat": {values: map[string]float64{key.container: usage}}, "memory_coverage": {values: map[string]float64{key.container: 2016}},
				"restart_activity": {values: map[string]float64{key.container: 0}}, "termination_history": {terminations: map[string]terminationEvidence{key.container: tt.ksm}},
			})
			fleet := buildScanRow(container, "memory", key, 2016, w, scanBatchEvidence{memory: map[scanKey][]float64{key: repeatedValues(usage)}, restarts: map[scanKey]float64{key: 0}, terminations: map[scanKey]terminationEvidence{key: tt.ksm}})
			for _, row := range []RightsizingRow{detail, fleet} {
				if row.RecommendationReason != tt.wantReason || (row.RecommendedReq != nil) != (tt.wantReason == "") {
					t.Fatalf("reason=%q recommendation=%v, want reason=%q", row.RecommendationReason, row.RecommendedReq, tt.wantReason)
				}
			}
		})
	}
}

func TestMeasuredRestartsKeepHistoricalEvidencePolicy(t *testing.T) {
	now := time.Now()
	w := rightsizingWorkload{}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "local", CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))}}
	pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "app", RestartCount: 1}}
	w.observePodTerminations(pod, now)
	if w.hasOOMEvidence("app", 0, true, terminationEvidence{}) {
		t.Fatal("missed the restart of a native sidecar")
	}
	if !w.hasOOMEvidence("app", 2, true, terminationEvidence{Any: true}) {
		t.Fatal("discarded observed restart and termination history")
	}
	if w.hasOOMEvidence("app", 2, true, terminationEvidence{}) {
		t.Fatal("accepted restarts without termination history")
	}
	if !w.hasOOMEvidence("sidecar", 0, true, terminationEvidence{}) {
		t.Fatal("one container's restarts affected its sibling")
	}
}
