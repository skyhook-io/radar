package main

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/rightsizing"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func shadowCase(res, request string, observed float64, days int) Case {
	at := time.Date(2026, 11, 20, 12, 2, 0, 0, time.UTC)
	e := &Evidence{ConfigSince: at.AddDate(0, 0, -14), DirectSource: true, Representative: true}
	end := at.Truncate(time.Hour)
	for t := end.Add(-time.Duration(days) * 24 * time.Hour); t.Before(end); t = t.Add(5 * time.Minute) {
		e.SourceTimes = append(e.SourceTimes, t)
	}
	e.SourceTimes = append(e.SourceTimes, at)
	row := rightsizing.Row{
		Resource: res, Container: "worker", Confidence: rightsizing.ConfidenceMedium,
		HPAEvidenceAvailable: true, OOMEvidenceAvailable: true,
		SampleCount: len(e.SourceTimes), ExpectedSamples: 2016,
		Observed: &rightsizing.ObservedStatistic{Value: observed},
	}
	if res == "cpu" {
		row.Peak = &rightsizing.ObservedStatistic{Value: observed}
	}
	var req *resource.Quantity
	if request != "" {
		q := resource.MustParse(request)
		req = &q
	}
	rightsizing.SetCurrentQuantities(&row, req, nil, res)
	rightsizing.ClassifyRightsizingFit(&row, observed, req, nil, res)
	qos := rightsizing.PodQoS{}
	qos.Observe(corev1.PodQOSBurstable)
	rows := []rightsizing.Row{row}
	rightsizing.Finalize(rows, nil, qos, false)
	return Case{At: at, Workload: "Deployment/app/worker", Replicas: 1, Row: rows[0], Evidence: e}
}

func TestHistoryRamp(t *testing.T) {
	for _, tt := range []struct {
		days, minDays, next int
		status              string
	}{
		{1, 1, 886, "cut"}, {1, 2, 0, "need_data"}, {2, 2, 772, "cut"},
		{2, 3, 0, "need_data"}, {3, 3, 658, "cut"}, {7, 2, 200, "cut"},
	} {
		c := shadowCase("cpu", "1", .17, tt.days)
		r := Evaluate(c, tt.minDays)
		if r.Status != tt.status || r.EffectiveDays == nil || *r.EffectiveDays != float64(tt.days) ||
			r.Current == nil || *r.Current != 1000 || r.Baseline == nil || *r.Baseline != 500 ||
			r.Target == nil || *r.Target != 200 || r.Unit != "m" {
			t.Fatalf("D=%d min=%d: %+v", tt.days, tt.minDays, r)
		}
		if tt.next == 0 && r.Next != nil || tt.next > 0 && (r.Next == nil || *r.Next != float64(tt.next)) {
			t.Fatalf("D=%d min=%d next=%v want %d", tt.days, tt.minDays, r.Next, tt.next)
		}
	}
}

func TestDistinctUTCSlotsAndRecentWindow(t *testing.T) {
	c := shadowCase("cpu", "1", .17, 7)
	// Boundaries stay on the UTC hour even in non-integer-hour timezones.
	c.At = c.At.In(time.FixedZone("Nepal", 5*60*60+45*60))
	end := c.At.UTC().Truncate(time.Hour)
	samples := slices.Clone(c.Evidence.SourceTimes)
	tests := []struct {
		name         string
		keep         func(time.Time) bool
		days, recent float64
		status       string
	}{
		{"old outage", func(t time.Time) bool { return t.Before(end.Add(-72*time.Hour)) || !t.Before(end.Add(-67*time.Hour)) }, 6, 1, "cut"},
		{"daily blind spot", func(t time.Time) bool { return t.Hour() >= 4 }, 0, 20.0 / 24, "need_data"},
		{"weekday only", func(t time.Time) bool { return t.Weekday() != time.Saturday && t.Weekday() != time.Sunday }, 5, 1, "cut"},
		{"nine slots every hour", func(t time.Time) bool { return t.Minute() < 45 }, 6.3, .75, "cut"},
		{"eight slots every hour", func(t time.Time) bool { return t.Minute() < 40 }, 5.6, 8.0 / 12, "need_data"},
		{"one fresh after silence", func(t time.Time) bool { return t.Equal(c.At) }, 0, 0, "need_data"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c.Evidence.SourceTimes = nil
			for _, ts := range samples {
				if tt.keep(ts) {
					c.Evidence.SourceTimes = append(c.Evidence.SourceTimes, ts, ts)
				}
			}
			r := Evaluate(c, 2)
			if r.Status != tt.status || *r.EffectiveDays != tt.days || *r.RecentCoverage != tt.recent {
				t.Fatalf("status=%s D=%f recent=%f reasons=%v", r.Status, *r.EffectiveDays, *r.RecentCoverage, r.Reasons)
			}
			if tt.name == "old outage" && *r.Next != 315 {
				t.Fatalf("next=%f", *r.Next)
			}
		})
	}
}

func TestRequestChangeRestartsCreditAndTarget(t *testing.T) {
	c := shadowCase("cpu", "1", .17, 7)
	c.Evidence.ConfigSince = c.At.Truncate(time.Hour).Add(-48 * time.Hour)
	r := Evaluate(c, 2)
	if *r.EffectiveDays != 2 || r.Next != nil || !slices.Contains(r.Reasons, "target_configuration_unverified") {
		t.Fatalf("old config used: %+v", r)
	}
	c.Evidence.TargetCurrentConfigVerified = true
	r = Evaluate(c, 2)
	if r.Status != "cut" || *r.Next != 772 {
		t.Fatalf("verified target: %+v", r)
	}
	c.Evidence.ConfigSince = c.Evidence.ConfigSince.Add(time.Nanosecond)
	// With nine other occupied slots there is still full hour credit.
	c.Evidence.SourceTimes = slices.DeleteFunc(c.Evidence.SourceTimes, func(ts time.Time) bool { return ts.Minute() >= 50 })
	r = Evaluate(c, 2)
	if *r.EffectiveDays != 1.9 || r.Next != nil {
		t.Fatalf("pre-config timestamp credited: %+v", r)
	}
}

func TestFreshnessWithheldWithoutLosingBaseline(t *testing.T) {
	for _, req := range []string{"10m", ""} {
		c := shadowCase("cpu", req, .2, 2)
		exact := c.At.Add(-time.Hour)
		c.Evidence.SourceTimes = []time.Time{exact}
		r := Evaluate(c, 3)
		want := "increase"
		if req == "" {
			want = "missing_request"
		}
		if r.Status != want || r.Next == nil || r.VisibleSavings != 0 {
			t.Fatalf("fresh %q: %+v", req, r)
		}
		c.Evidence.SourceTimes = []time.Time{exact.Add(-time.Nanosecond), c.At.Add(time.Nanosecond)}
		r = Evaluate(c, 2)
		if r.EvidenceStatus != "stale" || r.Next != nil || r.Baseline == nil || r.Status != "need_data" {
			t.Fatalf("stale %q: %+v", req, r)
		}
		c.Evidence = nil
		r = Evaluate(c, 2)
		if r.Next != nil || r.RecentCoverage != nil || r.LastSeen != nil || r.Baseline == nil {
			t.Fatalf("absent %q: %+v", req, r)
		}
	}
}

func TestProvenanceAndIndependentSafety(t *testing.T) {
	for _, tt := range []struct {
		name, reason string
		change       func(*Case)
	}{
		{"missing fresh replica", "replica_coverage_unverified", func(c *Case) { c.Evidence.Representative = false }},
		{"evaluation grid", "source_provenance_unverified", func(c *Case) { c.Evidence.DirectSource = false }},
		{"config unknown", "configuration_unverified", func(c *Case) { c.Evidence.ConfigSince = time.Time{} }},
		{"provider failure", "evidence_unavailable", func(c *Case) { c.Evidence.Issue = "source timeout" }},
		{"no direct samples", "no_source_samples", func(c *Case) { c.Evidence.SourceTimes = nil }},
		{"zero replicas", "no_positive_replica_count", func(c *Case) { c.Replicas = 0 }},
		{"finalized safety blocker", "guaranteed_qos", func(c *Case) {
			c.Row.Decision = rightsizing.Decision{Status: rightsizing.DecisionReview, Reasons: []string{"guaranteed_qos"}}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := shadowCase("cpu", "1", .17, 7)
			tt.change(&c)
			r := Evaluate(c, 2)
			if r.Next != nil || r.VisibleSavings != 0 || !slices.Contains(r.Reasons, tt.reason) {
				t.Fatalf("%+v", r)
			}
		})
	}
	c := shadowCase("memory", "256Mi", 0, 7)
	c.Row.CurrentPodOOM = true
	c.Row.QueryError = "usage query failed"
	c.Row.Decision = rightsizing.Decision{Status: rightsizing.DecisionNone}
	c.Evidence = nil
	r := Evaluate(c, 2)
	if r.Status != "review" || !slices.Contains(r.Reasons, "current_pod_oom") || !slices.Contains(r.Reasons, "usage_query_failed") || slices.Contains(r.Reasons, c.Row.QueryError) {
		t.Fatalf("%+v", r)
	}
}

func TestQuotaUnknownAndMeasuredZero(t *testing.T) {
	c := shadowCase("cpu", "1", 0, 7)
	limit := 1.0
	c.Row.CurrentLimitValue = &limit
	r := Evaluate(c, 2)
	if r.Status != "review" || !slices.Contains(r.Reasons, "throttling_unknown_under_quota") || r.Next != nil {
		t.Fatalf("unknown: %+v", r)
	}
	quiet := 0.0
	c.Row.ThrottleRatio = &quiet
	c.Row.ThrottleAvailable = true
	r = Evaluate(c, 2)
	if r.Status != "cut" || *r.Target != 10 {
		t.Fatalf("measured zero: %+v", r)
	}
}

func TestMaterialityOfRoundedStep(t *testing.T) {
	for _, tt := range []struct {
		res, req       string
		days, replicas int
		next, savings  float64
		status         string
	}{
		{"cpu", "20m", 7, 1, 10, 0, "immaterial"},
		{"cpu", "20m", 7, 5, 10, 50, "cut"},
		{"cpu", "20m", 3, 5, 16, 0, "immaterial"},
		{"cpu", "80m", 2, 3, 60, 60, "cut"}, // exact divisibility at two days
		{"memory", "128Mi", 7, 1, 64, 64, "cut"},
		{"memory", "134217727", 7, 1, 64, 0, "immaterial"}, // exactly 1 byte below 128Mi
		{"memory", "134217729", 3, 1, 101, 0, "immaterial"},
	} {
		c := shadowCase(tt.res, tt.req, 0, tt.days)
		c.Replicas = tt.replicas
		r := Evaluate(c, 2)
		if r.Status != tt.status || r.Next == nil || *r.Next != tt.next || r.VisibleSavings != tt.savings {
			t.Fatalf("%s %s/%d x%d: %+v", tt.res, tt.req, tt.days, tt.replicas, r)
		}
	}
}

func TestInvalidQuantityAndUnknownDecision(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), -1, .0001, math.MaxFloat64} {
		c := shadowCase("cpu", "1", .17, 7)
		c.Row.CurrentRequestValue = &v
		r := Evaluate(c, 2)
		if r.Status != "invalid" || r.Next != nil || r.Current != nil || !slices.Contains(r.Reasons, "invalid_measurement") {
			t.Fatalf("current=%f: %+v", v, r)
		}
	}
	c := shadowCase("cpu", "1", .17, 7)
	c.Row.Decision = rightsizing.Decision{}
	if r := Evaluate(c, 2); r.Status != "invalid" || r.Next != nil {
		t.Fatalf("%+v", r)
	}
}

func TestBalancedAndExpiredDecisions(t *testing.T) {
	c := shadowCase("cpu", "1", .8, 7)
	if r := Evaluate(c, 2); r.Status != "none" || r.Next != nil {
		t.Fatalf("balanced row: %+v", r)
	}
	c.At = c.At.Add(time.Hour + time.Second)
	if r := Evaluate(c, 2); r.Status != "need_data" || r.EvidenceStatus != "stale" {
		t.Fatalf("expired balanced row: %+v", r)
	}
	c = shadowCase("cpu", "1", .17, 7)
	c.At = c.At.Add(time.Hour + time.Second)
	if r := Evaluate(c, 2); r.Status != "need_data" || r.Next != nil || r.Baseline == nil || r.VisibleSavings != 0 {
		t.Fatalf("expired cut: %+v", r)
	}
	c = shadowCase("cpu", "15m", 0, 1)
	if r := Evaluate(c, 1); r.Status != "none" || r.Next != nil || !slices.Contains(r.Reasons, "no_whole_unit_cut") {
		t.Fatalf("zero rounded step: %+v", r)
	}
}

func TestSafetyAndEvidencePrecedence(t *testing.T) {
	for _, tt := range []struct {
		name, status, reason string
		change               func(*Case)
	}{
		{"fresh but zero replicas", "review", "no_positive_replica_count", func(c *Case) { c.Replicas = 0 }},
		{"partial replica coverage", "need_data", "replica_coverage_unverified", func(c *Case) { c.Evidence.Representative = false }},
		{"source failure", "need_data", "evidence_unavailable", func(c *Case) { c.Evidence.Issue = "backend connection timed out" }},
		{"stale measurements", "need_data", "stale", func(c *Case) { c.Evidence.SourceTimes = []time.Time{c.At.Add(-90 * time.Minute)} }},
		{"baseline safety review", "review", "guaranteed_qos", func(c *Case) {
			c.Row.Decision = rightsizing.Decision{Status: rightsizing.DecisionReview, Reasons: []string{"guaranteed_qos"}}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := shadowCase("cpu", "1", .17, 7)
			c.Evidence.ConfigSince = c.At.Add(-2 * 24 * time.Hour)
			tt.change(&c)
			r := Evaluate(c, 2)
			if r.Status != tt.status || !slices.Contains(r.Reasons, tt.reason) || slices.Contains(r.Reasons, "target_configuration_unverified") || r.Detail != c.Evidence.Issue || slices.Contains(r.Reasons, r.Detail) {
				t.Fatalf("%+v", r)
			}
		})
	}
	c := shadowCase("cpu", "1", .8, 7)
	c.Evidence.ConfigSince = c.At.Add(-time.Hour)
	if r := Evaluate(c, 2); r.Status != "none" || len(r.Reasons) > 0 {
		t.Fatalf("balanced row has no target to check: %+v", r)
	}
}

func TestIncreaseWithCPUQuota(t *testing.T) {
	quiet, throttled := 0.0, 0.3
	for _, ratio := range []*float64{nil, &quiet, &throttled} {
		c := shadowCase("cpu", "100m", .2, 7)
		limit := 1.0
		c.Row.CurrentLimitValue = &limit
		c.Row.ThrottleAvailable = ratio != nil
		c.Row.ThrottleRatio = ratio
		c.Row.Bursty = true
		r := Evaluate(c, 2)
		if r.Status != "increase" || r.Next == nil || *r.Next != 250 || len(r.Reasons) > 0 {
			t.Fatalf("throttle=%v: %+v", ratio, r)
		}
		c.Evidence.ConfigSince = c.At.Add(-48 * time.Hour)
		if r := Evaluate(c, 2); r.Status != "need_data" || !slices.Contains(r.Reasons, "target_configuration_unverified") {
			t.Fatalf("mixed-config increase: %+v", r)
		}
	}
	c := shadowCase("cpu", "1", .17, 7)
	limit := 1.0
	c.Row.CurrentLimitValue = &limit
	c.Evidence.SourceTimes = []time.Time{c.At}
	if r := Evaluate(c, 2); r.Status != "need_data" || slices.Contains(r.Reasons, "throttling_unknown_under_quota") {
		t.Fatalf("ineligible cut: %+v", r)
	}
}

func TestIncompleteUsagePreservesIndependentRisks(t *testing.T) {
	c := shadowCase("memory", "256Mi", 0, 2)
	c.Row = rightsizing.Row{Container: "worker", Resource: "memory", Fit: rightsizing.FitInsufficientHistory, HPAManaged: true,
		CurrentPodOOM: true, WindowOOMEvidence: true, Decision: rightsizing.Decision{Status: rightsizing.DecisionNone}}
	r := Evaluate(c, 2)
	if r.Status != "review" || !slices.Equal(r.Reasons, []string{"insufficient_usage", "hpa_managed", "current_pod_oom", "oom_in_window"}) || r.Next != nil {
		t.Fatalf("%+v", r)
	}
}

func TestInconsistentFitAndInvalidQuota(t *testing.T) {
	for _, tt := range []struct {
		reason string
		change func(*rightsizing.Row)
	}{
		{"invalid_fit", func(r *rightsizing.Row) { r.Fit = "unknown" }},
		{"inconsistent_decision", func(r *rightsizing.Row) { r.Decision.Status = rightsizing.DecisionNone }},
		{"inconsistent_decision", func(r *rightsizing.Row) { r.CalculatedRequestValue = nil }},
		{"invalid_increase", func(r *rightsizing.Row) { r.Fit = rightsizing.FitUnderRequested }},
		{"invalid_measurement", func(r *rightsizing.Row) { v := math.Inf(1); r.CurrentLimitValue = &v }},
	} {
		c := shadowCase("cpu", "1", .17, 7)
		tt.change(&c.Row)
		r := Evaluate(c, 2)
		if r.Status != "invalid" || !slices.Contains(r.Reasons, tt.reason) || slices.Contains(r.Reasons, "throttling_unknown_under_quota") || r.Next != nil {
			t.Fatalf("%+v", r)
		}
	}
	c := shadowCase("cpu", "1", .17, 7)
	v := math.NaN()
	c.Row.CurrentLimitValue = &v
	c.Row.Decision = rightsizing.Decision{Status: rightsizing.DecisionReview, Reasons: []string{"guaranteed_qos"}}
	r := Evaluate(c, 2)
	if r.Status != "invalid" || !slices.Contains(r.Reasons, "guaranteed_qos") {
		t.Fatalf("original safety decision lost: %+v", r)
	}
}
