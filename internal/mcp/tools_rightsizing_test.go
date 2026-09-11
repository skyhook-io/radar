package mcp

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"

	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
)

func TestGetRightsizingRequiresScope(t *testing.T) {
	_, _, err := handleGetRightsizing(context.Background(), nil, getRightsizingInput{})
	if err == nil {
		t.Fatal("scope must be required so a bare call never triggers the cluster scan")
	}
	for _, want := range []string{"workload", "namespace", "cluster", "45s"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should teach all three call shapes and the scan's cost, missing %q: %v", want, err)
		}
	}
}

func TestGetRightsizingRejectsUnknownScope(t *testing.T) {
	_, _, err := handleGetRightsizing(context.Background(), nil, getRightsizingInput{Scope: "fleet"})
	if err == nil || !strings.Contains(err.Error(), "fleet") {
		t.Fatalf("expected the rejected scope to be named back, got: %v", err)
	}
}

func TestGetRightsizingClusterScopeRejectsNarrowingParams(t *testing.T) {
	for _, input := range []getRightsizingInput{
		{Scope: "cluster", Namespace: "prod"},
		{Scope: "cluster", Kind: "Deployment"},
		{Scope: "cluster", Name: "checkout"},
	} {
		_, _, err := handleGetRightsizing(context.Background(), nil, input)
		if err == nil {
			t.Errorf("scope=cluster with %+v should be rejected rather than silently ignoring the narrowing", input)
		}
	}
}

func TestGetRightsizingNamespaceScopeRequiresNamespace(t *testing.T) {
	_, _, err := handleGetRightsizing(context.Background(), nil, getRightsizingInput{Scope: "namespace"})
	if err == nil || !strings.Contains(err.Error(), "cluster") {
		t.Fatalf("expected an error routing to scope=cluster for a whole-cluster scan, got: %v", err)
	}
}

func TestGetRightsizingWorkloadScopeRequiresIdentifiers(t *testing.T) {
	for _, input := range []getRightsizingInput{
		{Scope: "workload"},
		{Scope: "workload", Kind: "Deployment"},
		{Scope: "workload", Kind: "Deployment", Namespace: "prod"},
	} {
		_, _, err := handleGetRightsizing(context.Background(), nil, input)
		if err == nil {
			t.Errorf("scope=workload with %+v should demand kind, namespace, and name", input)
		}
	}
}

func rightsizingRow(fit prometheuspkg.RightsizingFit, current, recommended float64) prometheuspkg.RightsizingRow {
	recommendedStr := "recommended"
	row := prometheuspkg.RightsizingRow{
		Container:      "app",
		Resource:       "cpu",
		Fit:            fit,
		Confidence:     prometheuspkg.ConfidenceHigh,
		RecommendedReq: &recommendedStr,
	}
	// A missing request has no current value at all, which is what the ranker
	// keys on — so only populate it when there is one.
	if current > 0 {
		currentStr := "current"
		row.CurrentRequest = &currentStr
		row.CurrentRequestValue = &current
	}
	if recommended > 0 {
		row.RecommendedRequestValue = &recommended
	}
	return row
}

func TestFilterRightsizingRowsDropsBalancedByDefault(t *testing.T) {
	rows := []prometheuspkg.RightsizingRow{
		rightsizingRow(prometheuspkg.FitBalanced, 1, 1),
		rightsizingRow(prometheuspkg.FitOversized, 4, 1),
		rightsizingRow(prometheuspkg.FitUnderRequested, 1, 4),
		rightsizingRow(prometheuspkg.FitMissingRequest, 0, 2),
		rightsizingRow(prometheuspkg.FitInsufficientHistory, 1, 1),
	}

	filtered := filterRightsizingRows(rows, false)
	actionable, omitted := filtered.rows, filtered.omitted
	if len(actionable) != 3 {
		t.Fatalf("expected only oversized, under_requested, and missing_request, got %d", len(actionable))
	}
	for _, row := range actionable {
		if row.Fit == prometheuspkg.FitBalanced || row.Fit == prometheuspkg.FitInsufficientHistory {
			t.Errorf("non-actionable fit %q leaked into the default response", row.Fit)
		}
	}

	if omitted.Balanced != 1 || omitted.InsufficientHistory != 1 {
		t.Errorf("omissions must be reported so the model can see rows were withheld, got %+v", omitted)
	}

	includeAll := filterRightsizingRows(rows, true)
	all, allOmitted := includeAll.rows, includeAll.omitted
	if len(all) != len(rows) {
		t.Errorf("include_balanced should return every row, got %d of %d", len(all), len(rows))
	}
	if allOmitted.total() != 0 {
		t.Errorf("nothing is withheld when include_balanced is set, got %+v", allOmitted)
	}
}

func TestFilterRightsizingRowsFormatsThrottleOnlyWhenMeasured(t *testing.T) {
	ratio := 0.125
	measured := rightsizingRow(prometheuspkg.FitOversized, 4, 1)
	measured.ThrottleAvailable = true
	measured.ThrottleRatio = &ratio

	unmeasured := rightsizingRow(prometheuspkg.FitOversized, 4, 1)
	unmeasured.ThrottleRatio = &ratio

	out := filterRightsizingRows([]prometheuspkg.RightsizingRow{measured, unmeasured}, false).rows
	if out[0].ThrottleRatio == nil || *out[0].ThrottleRatio != "12.5%" {
		t.Errorf("measured throttling should be formatted as a percentage, got %v", out[0].ThrottleRatio)
	}
	if out[1].ThrottleRatio != nil {
		t.Error("an unavailable throttle ratio must stay absent — unavailable is not zero")
	}
}

func TestRequestChangeRatioIsDirectionless(t *testing.T) {
	up := requestChangeRatio(rightsizingRow(prometheuspkg.FitUnderRequested, 1, 4))
	down := requestChangeRatio(rightsizingRow(prometheuspkg.FitOversized, 4, 1))
	if up != 4 || down != 4 {
		t.Errorf("a 4x change should rank the same in both directions, got up=%v down=%v", up, down)
	}
	if got := requestChangeRatio(rightsizingRow(prometheuspkg.FitMissingRequest, 0, 2)); got != 0 {
		t.Errorf("a missing current request has no ratio, got %v", got)
	}
}

func TestWorkloadRequestDeltaRanksMissingRequestsFirst(t *testing.T) {
	missing := filterRightsizingRows(
		[]prometheuspkg.RightsizingRow{rightsizingRow(prometheuspkg.FitMissingRequest, 0, 2)}, false)

	large := filterRightsizingRows(
		[]prometheuspkg.RightsizingRow{rightsizingRow(prometheuspkg.FitOversized, 100, 1)}, false)

	if !math.IsInf(missing.requestDelta, 1) {
		t.Error("a missing request has no ratio and should outrank every proportional change")
	}
	if got := large.requestDelta; got != 100 {
		t.Errorf("expected the 100x reduction to rank at 100, got %v", got)
	}
}

func TestRightsizingGuidanceWarnsOnPartialScans(t *testing.T) {
	complete := rightsizingGuidance(prometheuspkg.RightsizingScanComplete, false, "cluster", nil)
	if strings.Contains(complete, "partial") {
		t.Errorf("a complete scan should not be described as partial: %q", complete)
	}
	if !strings.Contains(complete, "confidence") || !strings.Contains(complete, "7 days") {
		t.Errorf("guidance must state the 7-day window and the confidence caveat: %q", complete)
	}
	if !strings.Contains(complete, "include_balanced") {
		t.Errorf("guidance should say correctly-sized rows were omitted: %q", complete)
	}

	partial := rightsizingGuidance(prometheuspkg.RightsizingScanPartial, true, "cluster", nil)
	if !strings.Contains(partial, "cluster-wide") {
		t.Errorf("a partial scan must warn against cluster-wide conclusions: %q", partial)
	}
	if strings.Contains(partial, "include_balanced") {
		t.Errorf("include_balanced=true should not carry the omission note: %q", partial)
	}
}

func TestRightsizingUnavailableCarriesRemediation(t *testing.T) {
	resp := rightsizingUnavailable("cluster", "prometheus_unavailable")
	if resp.State != prometheuspkg.RightsizingScanUnavailable {
		t.Errorf("unexpected state %q", resp.State)
	}
	if !strings.Contains(resp.Remediation, "kube-state-metrics") {
		t.Errorf("remediation should name the history source recommendations need, got: %q", resp.Remediation)
	}
	if resp.Workloads == nil {
		t.Error("workloads should serialize as an empty array, not null")
	}
}

func TestRightsizingScanKindsComeFromTheSharedCatalogue(t *testing.T) {
	// The previous version of this test compared against a literal of its own,
	// so a kind added to the REST scan and not to MCP still passed. Both
	// surfaces now range over prometheuspkg.RightsizingScanKinds, which is what
	// actually makes drift impossible; this pins the catalogue's contents.
	want := map[string]string{"Deployment": "deployments", "StatefulSet": "statefulsets", "DaemonSet": "daemonsets"}
	if len(prometheuspkg.RightsizingScanKinds) != len(want) {
		t.Fatalf("scan catalogue changed: %+v", prometheuspkg.RightsizingScanKinds)
	}
	for _, kind := range prometheuspkg.RightsizingScanKinds {
		if want[kind.Kind] != kind.Resource {
			t.Errorf("kind %q maps to resource %q, expected %q", kind.Kind, kind.Resource, want[kind.Kind])
		}
	}
}

func TestRightsizingRemediationCoversTheScanDeadline(t *testing.T) {
	// The scan budget now covers authorization, so a deadline can be reported
	// where only access_denied used to be reachable.
	if rightsizingRemediation("scan_deadline_exceeded") == "" {
		t.Error("a blown scan budget reaches the model with no remediation")
	}
}

func TestRightsizingRemediationCoversScanFailureReasons(t *testing.T) {
	// The scan reports its own vocabulary; an unmapped reason leaves the model
	// with a bare code and nothing to tell the user to do about it.
	for _, reason := range []string{
		"prometheus_unavailable",
		"owner_metrics_query_failed",
		"owner_metrics_missing",
		"deployment_owner_metrics_missing",
		"resource_cache_unavailable",
		"workload_kinds_unavailable",
		"access_denied",
	} {
		if rightsizingRemediation(reason) == "" {
			t.Errorf("reason %q reaches the model with no remediation", reason)
		}
	}
	if rightsizingRemediation("something_new") != "" {
		t.Error("unknown reasons should get no invented remediation")
	}
}

func TestGetRightsizingRejectsUnsupportedKindBeforeAuthorizing(t *testing.T) {
	// A SAR against apps/pods denies on a resource that cannot exist, which
	// would answer "forbidden" to what is really an unsupported-kind mistake.
	_, _, err := handleGetRightsizing(context.Background(), nil, getRightsizingInput{
		Scope: "workload", Kind: "Pod", Namespace: "cost-demo", Name: "x",
	})
	if err == nil {
		t.Fatal("expected Pod to be rejected")
	}
	if strings.Contains(err.Error(), "forbidden") {
		t.Errorf("unsupported kind must not surface as an authorization failure: %v", err)
	}
	if !strings.Contains(err.Error(), "container template") {
		t.Errorf("error should explain why Pods are the wrong granularity: %v", err)
	}
}

func TestRightsizingPartialGuidancePointsAtEveryCause(t *testing.T) {
	// partial is set for restricted/unavailable kinds and warnings with every
	// batch complete, so naming only the batch counters reads as reassurance.
	partial := rightsizingGuidance(prometheuspkg.RightsizingScanPartial, false, "cluster", nil)
	for _, cause := range []string{"restrictedKinds", "unavailableKinds", "completedBatches", "warnings"} {
		if !strings.Contains(partial, cause) {
			t.Errorf("partial guidance omits %q, so that cause reads as complete coverage: %q", cause, partial)
		}
	}
}

func TestNarrowedClusterScanGuidanceRefusesClusterWideFraming(t *testing.T) {
	// scope="cluster" resolves to what the identity can list, or to the
	// --namespace pin. A complete-looking scan over two namespaces must not
	// read as a cluster-wide answer.
	narrowed := rightsizingGuidance(prometheuspkg.RightsizingScanPartial, false, "cluster", []string{"prod", "staging"})
	if !strings.Contains(narrowed, "namespaceScope") {
		t.Errorf("guidance must point at the field naming the real scope: %q", narrowed)
	}
	if !strings.Contains(narrowed, "2 namespace") {
		t.Errorf("guidance must state how many namespaces were reached: %q", narrowed)
	}

	full := rightsizingGuidance(prometheuspkg.RightsizingScanComplete, false, "cluster", nil)
	if strings.Contains(full, "namespaceScope") {
		t.Errorf("an unnarrowed scan should carry no scope caveat: %q", full)
	}
}

func TestNarrowedClusterScopeRemediationExplainsBothCauses(t *testing.T) {
	remediation := rightsizingRemediation("namespace_scope_limited")
	if !strings.Contains(remediation, "--namespace") || !strings.Contains(remediation, "cluster-wide") {
		t.Errorf("remediation must separate the RBAC cause from the pin: %q", remediation)
	}
}

func TestOmissionCountersAreExclusive(t *testing.T) {
	// A query-error row already carries fit=insufficient_history; counting it in
	// both buckets would overstate how much history is missing.
	queryErr := rightsizingRow(prometheuspkg.FitInsufficientHistory, 1, 1)
	queryErr.QueryError = "upstream 500"

	omitted := filterRightsizingRows([]prometheuspkg.RightsizingRow{
		queryErr,
		rightsizingRow(prometheuspkg.FitInsufficientHistory, 1, 1),
		rightsizingRow(prometheuspkg.FitBalanced, 1, 1),
	}, false).omitted

	if omitted.QueryError != 1 || omitted.InsufficientHistory != 1 || omitted.Balanced != 1 {
		t.Errorf("categories must be exclusive, got %+v", omitted)
	}
	if omitted.total() != 3 {
		t.Errorf("every withheld row should be counted once, got %d", omitted.total())
	}
}

func TestIncompleteEvidenceReadsRawRowsNotSurvivors(t *testing.T) {
	// Derived from the raw rows so include_balanced=true, which withholds
	// nothing, still reports partial when a query failed.
	queryErr := rightsizingRow(prometheuspkg.FitInsufficientHistory, 1, 1)
	queryErr.QueryError = "upstream 500"
	rows := []prometheuspkg.RightsizingRow{rightsizingRow(prometheuspkg.FitOversized, 4, 1), queryErr}

	if !filterRightsizingRows(rows, true).incompleteEvidence {
		t.Error("a query error is incomplete evidence regardless of what survives filtering")
	}
	if filterRightsizingRows([]prometheuspkg.RightsizingRow{rightsizingRow(prometheuspkg.FitOversized, 4, 1)}, false).incompleteEvidence {
		t.Error("fully evidenced rows must not be reported as partial")
	}
}

func TestWorkloadRequestDeltaRanksBlockedRecommendationsLast(t *testing.T) {
	// A row with a request but no recommendation had its evidence blocked (HPA,
	// OOM, query error). It is less actionable than a real recommendation, so
	// it must not consume the limit ahead of one.
	blocked := rightsizingRow(prometheuspkg.FitOversized, 4, 0)
	blocked.RecommendedReq = nil
	blockedDelta := filterRightsizingRows([]prometheuspkg.RightsizingRow{blocked}, false).requestDelta
	realDelta := filterRightsizingRows([]prometheuspkg.RightsizingRow{rightsizingRow(prometheuspkg.FitOversized, 4, 1)}, false).requestDelta
	if math.IsInf(blockedDelta, 1) {
		t.Error("a blocked recommendation must not rank as infinity")
	}
	if blockedDelta >= realDelta {
		t.Errorf("blocked (%v) should rank below a real recommendation (%v)", blockedDelta, realDelta)
	}
}

func TestEvidenceAvailabilityFlagsSurvive(t *testing.T) {
	// hpaManaged=false with no HPA evidence means unknown, not "not autoscaled",
	// and a request cut is unsafe in both the OOM and HPA unknown cases.
	unknown := rightsizingRow(prometheuspkg.FitOversized, 4, 1)
	known := rightsizingRow(prometheuspkg.FitOversized, 4, 1)
	known.HPAEvidenceAvailable = true
	known.OOMEvidenceAvailable = true

	out := filterRightsizingRows([]prometheuspkg.RightsizingRow{unknown, known}, false).rows
	if out[0].HPAEvidenceAvailable || out[0].OOMEvidenceAvailable {
		t.Error("missing evidence must not be reported as available")
	}
	if !out[1].HPAEvidenceAvailable || !out[1].OOMEvidenceAvailable {
		t.Error("available evidence must be reported so the model can trust hpaManaged/OOM flags")
	}
}

func TestGetRightsizingNamespaceScopeRejectsWorkloadIdentifiers(t *testing.T) {
	for _, input := range []getRightsizingInput{
		{Scope: "namespace", Namespace: "prod", Kind: "Deployment"},
		{Scope: "namespace", Namespace: "prod", Name: "checkout"},
	} {
		_, _, err := handleGetRightsizing(context.Background(), nil, input)
		if err == nil {
			t.Errorf("%+v should be rejected rather than silently running a whole-namespace scan", input)
		} else if !strings.Contains(err.Error(), `scope="workload"`) {
			t.Errorf("error should route to the cheaper workload scope, got: %v", err)
		}
	}
}

func TestWorkloadScopeGuidanceDoesNotCiteScanCoverage(t *testing.T) {
	// A workload response carries no coverage object, so pointing the model at
	// coverage.completedBatches sends it to a field that is not there.
	workload := rightsizingGuidance(prometheuspkg.RightsizingScanPartial, false, "workload", nil)
	for _, absent := range []string{"completedBatches", "restrictedKinds", "unavailableKinds"} {
		if strings.Contains(workload, absent) {
			t.Errorf("workload guidance cites %q, which only exists on scan scopes: %q", absent, workload)
		}
	}
	if !strings.Contains(workload, "omitted") {
		t.Errorf("workload guidance should point at the omitted counts: %q", workload)
	}
}

func TestBlockedRowsRankLastEvenWhenRequestIsMissing(t *testing.T) {
	// Infinity means "maximally actionable", which requires something to act on.
	blocked := rightsizingRow(prometheuspkg.FitMissingRequest, 0, 0)
	blocked.RecommendedReq = nil
	blockedRows := filterRightsizingRows([]prometheuspkg.RightsizingRow{blocked}, false)
	if got := blockedRows.requestDelta; math.IsInf(got, 1) {
		t.Error("a missing request with no recommendation has nothing to act on and must not rank first")
	}

	actionable := filterRightsizingRows(
		[]prometheuspkg.RightsizingRow{rightsizingRow(prometheuspkg.FitMissingRequest, 0, 2)}, false)
	if got := actionable.requestDelta; !math.IsInf(got, 1) {
		t.Errorf("a missing request WITH a recommendation is maximally actionable, got %v", got)
	}
}

func TestExplicitZeroRequestRanksAsMissing(t *testing.T) {
	// An explicit 0 request has a non-nil CurrentRequest and no computable
	// ratio, so keying on nil alone would bury the strongest signal there is.
	zero := rightsizingRow(prometheuspkg.FitMissingRequest, 0, 2)
	explicit := "0"
	zero.CurrentRequest = &explicit
	zeroValue := 0.0
	zero.CurrentRequestValue = &zeroValue

	if got := filterRightsizingRows([]prometheuspkg.RightsizingRow{zero}, false).requestDelta; !math.IsInf(got, 1) {
		t.Errorf("an explicit zero request is still a missing request, got rank %v", got)
	}
}

func TestSuppressedRecommendationsCountAsIncompleteEvidence(t *testing.T) {
	// Radar sets these reasons only when the missing evidence actually blocked a
	// recommendation, so reporting state=complete would present a withheld
	// verdict as a clean one — and a memory cut on an unknown-OOM container is
	// exactly the unsafe change this guards.
	for _, reason := range []string{
		prometheuspkg.ReasonHPAEvidenceUnavailable,
		prometheuspkg.ReasonOOMEvidenceUnavailable,
	} {
		row := rightsizingRow(prometheuspkg.FitOversized, 4, 1)
		row.RecommendationReason = reason
		if !filterRightsizingRows([]prometheuspkg.RightsizingRow{row}, true).incompleteEvidence {
			t.Errorf("reason %q suppressed the recommendation and must not report as complete", reason)
		}
	}

	// A recommendation that stands on full evidence stays complete.
	fine := rightsizingRow(prometheuspkg.FitOversized, 4, 1)
	fine.RecommendationReason = "request_within_fit_range"
	if filterRightsizingRows([]prometheuspkg.RightsizingRow{fine}, true).incompleteEvidence {
		t.Error("a fully evidenced row must not downgrade the response to partial")
	}
}

// docsPath is the operator-facing contract for the rightsizing response.
const docsPath = "../../docs/mcp.md"

func TestEveryRecommendationReasonIsDocumented(t *testing.T) {
	// A bare reason string reaching an agent produces "no recommendation
	// available", which loses the safety reasoning that withheld it. The UI has
	// a sentence for each in getRightsizingExplanation; the MCP surface points
	// at docs/mcp.md instead, so every value has to appear there.
	raw, err := os.ReadFile(docsPath)
	if err != nil {
		t.Fatalf("read %s: %v", docsPath, err)
	}
	docs := string(raw)

	for _, reason := range []string{
		"insufficient_history",
		"request_within_fit_range",
		"hpa_managed",
		"hpa_evidence_unavailable",
		"oom_evidence",
		"oom_evidence_unavailable",
		"recommended_request_exceeds_limit",
	} {
		if !strings.Contains(docs, reason) {
			t.Errorf("recommendationReason %q reaches agents with nothing in %s explaining it", reason, docsPath)
		}
	}
}
