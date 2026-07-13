package prometheus

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/skyhook-io/radar/pkg/prom"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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
			classifyRightsizingFit(&row, tc.observed, tc.req, tc.lim, tc.resource)
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

func TestCalculatedRequestUsesPracticalScaleAwareSteps(t *testing.T) {
	tests := []struct {
		name     string
		observed float64
		resKind  string
		want     string
	}{
		{"cpu tiny usage keeps a 10m floor", 0.0001, "cpu", "10m"},
		{"cpu 100m demand uses a 50m step", 0.100, "cpu", "150m"},
		{"cpu 1 core demand uses a 500m step", 1.0, "cpu", "1.5"},
		{"cpu crossing one core rounds to half a core", 0.870, "cpu", "1.5"},
		{"cpu above four cores uses whole cores", 3.56, "cpu", "5"},
		{"memory tiny usage keeps a 64Mi floor", 1024, "memory", "64Mi"},
		{"memory 100Mi demand uses the preferred ladder", 100 * 1024 * 1024, "memory", "128Mi"},
		{"memory 1Gi demand rounds to 1.5Gi", 1024 * 1024 * 1024, "memory", "1.5Gi"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := calculatedRequest(tc.observed, tc.resKind)
			if got != tc.want {
				t.Errorf("calculatedRequest(%g, %q) = %q, want %q", tc.observed, tc.resKind, got, tc.want)
			}
		})
	}
}

func TestRecommendRequestStagesLargeReductions(t *testing.T) {
	tests := []struct {
		name         string
		observed     float64
		current      *resource.Quantity
		resourceName string
		conservative bool
		want         string
		wantLimited  bool
	}{
		{"200m CPU reduces no lower than one quarter", 0.001, mustQuantity(t, "200m"), "cpu", false, "50m", true},
		{"one CPU reduces no lower than one half", 0.001, mustQuantity(t, "1"), "cpu", false, "500m", true},
		{"small CPU can reduce to the 10m minimum", 0.001, mustQuantity(t, "50m"), "cpu", false, "10m", false},
		{"bursty CPU uses the one-half floor", 0.001, mustQuantity(t, "200m"), "cpu", true, "100m", true},
		{"memory reduces no lower than one half", 1024, mustQuantity(t, "1Gi"), "memory", false, "512Mi", true},
		{"missing request uses the demand target", 0.1, nil, "cpu", false, "150m", false},
		{"increase uses the demand target", 0.2, mustQuantity(t, "100m"), "cpu", false, "250m", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, limited := recommendRequest(tc.observed, tc.current, tc.resourceName, tc.conservative)
			if got != tc.want || limited != tc.wantLimited {
				t.Errorf("recommendRequest() = %q, %t; want %q, %t", got, limited, tc.want, tc.wantLimited)
			}
		})
	}
}

func TestIsBurstyCPURequiresMaterialAbsoluteAndRelativeGap(t *testing.T) {
	if !isBurstyCPU(0.02, 0.20) {
		t.Fatal("expected a material P95-to-P99 jump to be bursty")
	}
	if isBurstyCPU(0.10, 0.20) {
		t.Fatal("a two-fold jump must not be marked bursty")
	}
	if isBurstyCPU(0.001, 0.010) {
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

	response := computeRightsizing(context.Background(), querier, "Deployment", "argocd", "argocd-server", workload)
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
	selection, coverage := workloadSelection(context.Background(), querier, "Deployment", "prod", "api", []string{"api-6ccf7b8d9-x1"})
	if coverage != OwnerCoverageCurrentPods {
		t.Fatalf("coverage = %q, want current_pods", coverage)
	}
	if selection.podPattern != `^(api-6ccf7b8d9-x1)$` {
		t.Errorf("selection is not exact: %+v", selection)
	}
	if strings.Contains(selection.podPattern, "api-worker") || strings.Contains(selection.podPattern, `api-.*`) {
		t.Errorf("selection can collide with sibling workloads: %+v", selection)
	}
	if got := confidenceFor(2016, 1, coverage); got != ConfidenceMedium {
		t.Errorf("current-pod confidence = %q, want medium", got)
	}
	if got := confidenceFor(72, 72.0/2016.0, coverage); got != ConfidenceLow {
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

	response := computeRightsizing(context.Background(), querier, "Deployment", "prod", "api", workload)
	if response.Reason != "No workload usage samples are available in the last 7d." {
		t.Errorf("reason = %q, want missing usage samples", response.Reason)
	}
}

func TestOwnerSelectionDeduplicatesKSMTargets(t *testing.T) {
	query := ownerSelection("Deployment", "prod", "api")
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

func TestFormatRightsizingValue(t *testing.T) {
	tests := []struct {
		v       float64
		resKind string
		want    string
	}{
		{0.0005, "cpu", "10m"},
		{2.0, "cpu", "2"},
		{1.5, "cpu", "1.5"},
		{1024, "memory", "64Mi"},
		{0, "memory", "64Mi"},
		{float64(2 * 1024 * 1024 * 1024), "memory", "2Gi"},
		{1.0, "disk", ""},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			got := formatRightsizingValue(tc.v, tc.resKind)
			if got != tc.want {
				t.Errorf("formatRightsizingValue(%g, %q) = %q, want %q", tc.v, tc.resKind, got, tc.want)
			}
		})
	}
}

func TestFormatObservedValueDoesNotRoundLikeARecommendation(t *testing.T) {
	if got := formatObservedValue(0.0022, "cpu"); got != "2m" {
		t.Errorf("CPU observed = %q, want 2m", got)
	}
	if got := formatObservedValue(39.4*1024*1024, "memory"); got != "39Mi" {
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
