package mcp

import (
	"context"
	"reflect"
	"strings"
	"testing"

	pkgopencost "github.com/skyhook-io/radar/pkg/opencost"
)

func TestGetCostRejectsUnknownView(t *testing.T) {
	_, _, err := handleGetCost(context.Background(), nil, getCostInput{View: "spend"})
	if err == nil {
		t.Fatal("expected an error for an unknown view")
	}
	for _, want := range []string{"summary", "workloads", "nodes", "trend"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name the %q view so the model can recover, got: %v", want, err)
		}
	}
}

func TestGetCostWorkloadsViewRequiresNamespace(t *testing.T) {
	_, _, err := handleGetCost(context.Background(), nil, getCostInput{View: "workloads"})
	if err == nil {
		t.Fatal("expected an error when view=workloads has no namespace")
	}
	if !strings.Contains(err.Error(), "view=summary") {
		t.Errorf("error should route the model to summary to find a namespace, got: %v", err)
	}
}

func TestCostGuidanceFlagsPartialNamespaceScope(t *testing.T) {
	full := costGuidance(nil, "")
	if !strings.Contains(full, "730") {
		t.Errorf("guidance should state the monthly projection convention, got: %q", full)
	}
	if strings.Contains(full, "not the whole cluster") {
		t.Errorf("unscoped guidance should not claim a partial view, got: %q", full)
	}

	partial := costGuidance([]string{"a", "b"}, "")
	if !strings.Contains(partial, "2 namespace(s)") || !strings.Contains(partial, "not the whole cluster") {
		t.Errorf("scoped guidance should say totals cover only the readable namespaces, got: %q", partial)
	}

	// An explicit filter is the caller's own choice, not a permission limit.
	requested := costGuidance([]string{"prod"}, "prod")
	if !strings.Contains(requested, "only namespace prod") {
		t.Errorf("an explicitly requested namespace should be named, got: %q", requested)
	}
	if strings.Contains(requested, "this identity can read") {
		t.Errorf("an explicit filter must not be reported as an RBAC limitation, got: %q", requested)
	}
}

func TestCostRemediationExplainsMissingCostSource(t *testing.T) {
	if got := costRemediation(pkgopencost.ReasonNoPrometheus); !strings.Contains(got, "OpenCost") {
		t.Errorf("no_prometheus remediation should mention the cost source, got: %q", got)
	}
	if got := costRemediation(pkgopencost.ReasonNoMetrics); !strings.Contains(got, "OpenCost") {
		t.Errorf("no_metrics remediation should name what to install, got: %q", got)
	}
	if got := costRemediation("something_new"); got != "" {
		t.Errorf("unknown reasons should get no invented remediation, got: %q", got)
	}
}

func TestCostUnavailableCarriesRemediation(t *testing.T) {
	resp := costUnavailable("summary", pkgopencost.ReasonNoPrometheus, "USD")
	if resp.Available {
		t.Fatal("response should be unavailable")
	}
	if resp.Remediation == "" {
		t.Error("an unavailable response must say what is missing, or the agent reports the cluster has no cost data")
	}
	if resp.Currency != "USD" || resp.View != "summary" {
		t.Errorf("unexpected envelope: %+v", resp)
	}
}

func TestMonthlyProjectionMatchesCostsUI(t *testing.T) {
	if monthlyProjectionHours != 730 {
		t.Fatalf("the Costs UI projects hourly x 730; a different constant makes the tool disagree with the screen, got %d", monthlyProjectionHours)
	}
}

func TestGetCostNodesViewRejectsNamespace(t *testing.T) {
	_, _, err := handleGetCost(context.Background(), nil, getCostInput{View: "nodes", Namespace: "prod"})
	if err == nil {
		t.Fatal("node spend is cluster-wide; silently dropping the namespace lets an agent report it as namespace-scoped")
	}
	if !strings.Contains(err.Error(), "view=summary") {
		t.Errorf("error should route to a namespace-capable view, got: %v", err)
	}
}

func TestGetCostAdvertisesOnlyServableTrendRanges(t *testing.T) {
	// pkg/opencost resolves 6h and 7d and silently defaults everything else to
	// 24h, so advertising any other value promises data the backends cannot serve.
	field, ok := reflect.TypeFor[getCostInput]().FieldByName("Range")
	if !ok {
		t.Fatal("getCostInput has no Range field")
	}
	schema := field.Tag.Get("jsonschema")
	for _, unservable := range []string{"30d", "90d", "1y"} {
		if strings.Contains(schema, unservable) {
			t.Errorf("range schema advertises %q, which resolveTrendRange silently turns into 24h: %q", unservable, schema)
		}
	}
	for _, servable := range []string{"6h", "24h", "7d"} {
		if !strings.Contains(schema, servable) {
			t.Errorf("range schema omits the servable range %q: %q", servable, schema)
		}
	}
}

func TestGetCostRejectsUnservableTrendRange(t *testing.T) {
	_, _, err := handleGetCost(context.Background(), nil, getCostInput{View: "trend", Range: "30d"})
	if err == nil {
		t.Fatal("an unservable range must be rejected, not silently answered with 24h data")
	}
	for _, want := range []string{"6h", "24h", "7d"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name the servable ranges, missing %q: %v", want, err)
		}
	}
	if !pkgopencost.SupportedTrendRange("") || !pkgopencost.SupportedTrendRange("7d") {
		t.Error("empty and 7d are servable")
	}
	if pkgopencost.SupportedTrendRange("30d") {
		t.Error("30d resolves to 24h in both backends and must not be accepted")
	}
}

func TestCostRemediationCoversReachableReasons(t *testing.T) {
	for _, reason := range []string{
		pkgopencost.ReasonNoPrometheus, pkgopencost.ReasonNoCostSource, pkgopencost.ReasonNoMetrics,
		pkgopencost.ReasonAccessDenied, pkgopencost.ReasonAuthentication, pkgopencost.ReasonQueryError,
		pkgopencost.ReasonSourceUnavailable, pkgopencost.ReasonConfigMismatch, pkgopencost.ReasonDeploymentConfig,
		pkgopencost.ReasonInsufficientHistory, pkgopencost.ReasonHistoryUnsupported, pkgopencost.ReasonNotFound,
	} {
		if costRemediation(reason) == "" {
			t.Errorf("reason %q reaches the model with no remediation", reason)
		}
	}
}

func TestGetCostRejectsViewIncompatibleParams(t *testing.T) {
	// Accepting a parameter a view ignores lets an agent believe it filtered.
	for _, input := range []getCostInput{
		{View: "summary", Range: "7d"},
		{View: "nodes", Range: "6h"},
		{View: "trend", Limit: 10},
	} {
		if _, _, err := handleGetCost(context.Background(), nil, input); err == nil {
			t.Errorf("%+v should be rejected rather than silently ignoring the parameter", input)
		}
	}
	// The compatible combinations must still work through validation.
	if _, _, err := handleGetCost(context.Background(), nil, getCostInput{View: "trend", Range: "7d"}); err != nil {
		t.Errorf("trend accepts range: %v", err)
	}
}

func TestCostRemediationDistinguishesSources(t *testing.T) {
	// "no cost source at all" and "a source exists but has no metrics" need
	// different advice; sharing a string tells half of callers the wrong thing.
	noSource := costRemediation(pkgopencost.ReasonNoCostSource)
	noMetrics := costRemediation(pkgopencost.ReasonNoMetrics)
	if noSource == noMetrics {
		t.Error("no_cost_source and no_metrics describe different situations and must not share remediation")
	}
	if strings.Contains(noSource, "Prometheus is reachable") {
		t.Errorf("no_cost_source must not claim Prometheus is reachable: %q", noSource)
	}
	// Either source can reject credentials, so naming only one misdirects.
	auth := costRemediation(pkgopencost.ReasonAuthentication)
	if !strings.Contains(auth, "Kubecost") || !strings.Contains(auth, "Prometheus") {
		t.Errorf("authentication remediation should cover both sources: %q", auth)
	}
}

func TestEfficiencyGuidanceStatesTheDenominator(t *testing.T) {
	// An agent reading efficiency: 100 cannot tell "correctly sized" from
	// "consuming well above its request" — allocation is max(requested,
	// observed), so the ratio caps at 100. Nothing in the numbers says so.
	if !strings.Contains(costEfficiencyExplainer, "greater of requested and observed") {
		t.Error("guidance must state that the denominator is allocation, not the request")
	}
	if !strings.Contains(costEfficiencyExplainer, "caps at 100") {
		t.Error("guidance must state the ceiling, or 100 reads as a clean bill of health")
	}
	if !strings.Contains(costEfficiencyExplainer, "get_rightsizing") {
		t.Error("guidance must route the 'should this change' question to the tool that gates on OOM and HPA")
	}
}

func TestEfficiencyGuidanceRidesTheViewsThatEmitEfficiency(t *testing.T) {
	// summary carries clusterEfficiency plus per-namespace efficiency, and
	// workloads carries per-workload efficiency. nodes and trend carry neither,
	// so the explainer would be noise there.
	summary := costGuidance(nil, "") + " " + costEfficiencyExplainer
	if !strings.Contains(summary, costRateExplainer) || !strings.Contains(summary, costEfficiencyExplainer) {
		t.Error("summary guidance must carry both the rate and the efficiency explainer")
	}
}
