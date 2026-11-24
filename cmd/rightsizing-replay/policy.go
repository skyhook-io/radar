package main

import (
	"math"
	"math/bits"
	"slices"
	"time"

	"github.com/skyhook-io/radar/pkg/rightsizing"
)

type Case struct {
	At       time.Time       `json:"at"`
	Workload string          `json:"workload"`
	Replicas int             `json:"replicas"`
	Row      rightsizing.Row `json:"row"`
	Evidence *Evidence       `json:"evidence,omitempty"`
}

// Each entry attests to this case's workload, container, resource and active
// configuration. A collector supplies a source time only for slots where it
// verified all representative running replicas. Each replica must contribute
// a source time inside the same UTC slot. The oldest of their latest source
// times identifies the slot and bounds freshness for every replica.
type Evidence struct {
	ConfigSince                 time.Time   `json:"configSince"`
	DirectSource                bool        `json:"directSource"`
	Representative              bool        `json:"representative"`
	SourceTimes                 []time.Time `json:"sourceTimes"`
	TargetCurrentConfigVerified bool        `json:"targetCurrentConfigVerified,omitempty"`
	Issue                       string      `json:"issue,omitempty"`
}

type Result struct {
	eligibleReduction bool       // Also true when the candidate rounds back to the current value.
	Status            string     `json:"status"`
	Reasons           []string   `json:"reasons,omitempty"`
	Detail            string     `json:"detail,omitempty"`
	EvidenceStatus    string     `json:"evidenceStatus"`
	EffectiveDays     *float64   `json:"effectiveDays,omitempty"`
	RecentCoverage    *float64   `json:"recentCoverage,omitempty"`
	LastSeen          *time.Time `json:"lastSeen,omitempty"`
	Unit              string     `json:"unit"`
	Current           *float64   `json:"current,omitempty"`
	Baseline          *float64   `json:"baseline,omitempty"`
	Target            *float64   `json:"target,omitempty"`
	Next              *float64   `json:"next,omitempty"`
	VisibleSavings    float64    `json:"visibleSavings"`
}

// assessHistory returns tenths of a well-observed day. Keeping credits integral
// until reporting also makes the resource-unit ceiling exact at ties.
func assessHistory(c Case, r *Result) int {
	e := c.Evidence
	switch {
	case e == nil:
		r.EvidenceStatus = "evidence_unavailable"
	case e.Issue != "":
		r.EvidenceStatus = "evidence_unavailable"
		r.Detail = e.Issue
	case !e.DirectSource:
		r.EvidenceStatus = "source_provenance_unverified"
	case !e.Representative:
		r.EvidenceStatus = "replica_coverage_unverified"
	case e.ConfigSince.IsZero() || e.ConfigSince.After(c.At):
		r.EvidenceStatus = "configuration_unverified"
	}
	if r.EvidenceStatus != "" {
		r.Reasons = append(r.Reasons, r.EvidenceStatus)
		return 0
	}

	hoursEnd := c.At.UTC().Truncate(time.Hour)
	hoursStart := hoursEnd.Add(-168 * time.Hour)
	const slot = 5 * time.Minute
	recentEnd := c.At.UTC().Truncate(slot)
	recentStart := recentEnd.Add(-24 * time.Hour)
	var hours [168]uint16
	var recent [288]bool
	var latest time.Time
	for _, t := range e.SourceTimes {
		if t.IsZero() || t.Before(e.ConfigSince) || t.After(c.At) {
			continue
		}
		t = t.UTC()
		if t.After(latest) {
			latest = t
		}
		if !t.Before(hoursStart) && t.Before(hoursEnd) {
			idx := int(t.Sub(hoursStart) / slot)
			hours[idx/12] |= 1 << (idx % 12)
		}
		if !t.Before(recentStart) && t.Before(recentEnd) {
			recent[t.Sub(recentStart)/slot] = true
		}
	}
	var credits [24]int
	for i, slots := range hours {
		credits[i%24] += min(10, bits.OnesCount16(slots))
	}
	tenths := slices.Min(credits[:])
	days := float64(tenths) / 10
	r.EffectiveDays = &days
	count := 0
	for _, occupied := range recent {
		if occupied {
			count++
		}
	}
	density := float64(count) / float64(len(recent))
	r.RecentCoverage = &density
	if !latest.IsZero() {
		r.LastSeen = &latest
	}
	switch {
	case r.LastSeen == nil:
		r.EvidenceStatus = "no_source_samples"
	case c.At.Sub(*r.LastSeen) > time.Hour:
		r.EvidenceStatus = "stale"
	default:
		r.EvidenceStatus = "fresh"
	}
	if r.EvidenceStatus != "fresh" {
		r.Reasons = append(r.Reasons, r.EvidenceStatus)
	}
	return tenths
}

// nativeQuantity uses integer mCPU or bytes for arithmetic. Radar's wire
// quantities are cores or bytes; values above the exact float range cannot
// support a one-unit decision.
func nativeQuantity(v *float64, cpu bool) (int64, bool) {
	if v == nil || !finiteNonnegative(*v) {
		return 0, false
	}
	n := *v
	if cpu {
		n = math.Round(n * 1000)
		if n/1000 != *v {
			return 0, false
		}
	}
	if n > 1<<53-1 || math.Trunc(n) != n {
		return 0, false
	}
	return int64(n), true
}

func finiteNonnegative(n float64) bool {
	return n >= 0 && !math.IsInf(n, 0)
}

func materialSavings(current, next float64, replicas int, resource string) float64 {
	if replicas <= 0 || next >= current {
		return 0
	}
	savings := (current - next) * float64(replicas)
	if (resource == "cpu" && savings >= 50) || (resource == "memory" && savings >= 64) {
		return savings
	}
	return 0
}

func Evaluate(c Case, minDays int) Result {
	r := Result{Status: "need_data"}
	tenths := assessHistory(c, &r)
	row := c.Row
	unit := int64(1)
	cpu := row.Resource == "cpu"
	switch row.Resource {
	case "cpu":
		r.Unit = "m"
	case "memory":
		r.Unit = "Mi"
		unit = 1 << 20
	default:
		r.Status = "invalid"
		r.Reasons = append(r.Reasons, "unsupported_resource")
		return r
	}
	current, currentOK := nativeQuantity(row.CurrentRequestValue, cpu)
	target, targetOK := nativeQuantity(row.CalculatedRequestValue, cpu)
	baseline, baselineOK := nativeQuantity(row.RecommendedRequestValue, cpu)
	inUnits := func(n int64) *float64 {
		v := float64(n) / float64(unit)
		return &v
	}
	if currentOK {
		r.Current = inUnits(current)
	}
	if targetOK {
		r.Target = inUnits(target)
	}
	if baselineOK {
		r.Baseline = inUnits(baseline)
	}
	invalid := func(reason string) Result {
		r.Status = "invalid"
		r.Reasons = append(r.Reasons, reason)
		return r
	}
	r.Reasons = append(r.Reasons, row.Decision.Reasons...)
	_, limitOK := nativeQuantity(row.CurrentLimitValue, cpu)
	_, podRequestOK := nativeQuantity(row.PodLevelRequestValue, cpu)
	if (row.Observed != nil && !finiteNonnegative(row.Observed.Value)) ||
		(row.Peak != nil && !finiteNonnegative(row.Peak.Value)) ||
		(row.ThrottleRatio != nil && (!finiteNonnegative(*row.ThrottleRatio) || *row.ThrottleRatio > 1)) ||
		(row.CurrentLimitValue != nil && !limitOK) || (row.PodLevelRequestValue != nil && !podRequestOK) ||
		(row.CurrentRequestValue != nil && !currentOK) || (row.CalculatedRequestValue != nil && !targetOK) ||
		(row.RecommendedRequestValue != nil && !baselineOK) {
		return invalid("invalid_measurement")
	}
	switch row.Fit {
	case rightsizing.FitOversized, rightsizing.FitUnderRequested, rightsizing.FitMissingRequest,
		rightsizing.FitInsufficientHistory, rightsizing.FitBalanced, rightsizing.FitPodLevelRequest:
	default:
		return invalid("invalid_fit")
	}
	if row.Decision.Status == rightsizing.DecisionReview {
		r.Status = "review"
		return r
	}
	if row.QueryError != "" || row.Fit == rightsizing.FitInsufficientHistory {
		if row.Decision.Status != rightsizing.DecisionNone || baselineOK {
			return invalid("inconsistent_decision")
		}
		if row.QueryError != "" {
			r.Reasons = append(r.Reasons, "usage_query_failed")
		} else {
			r.Reasons = append(r.Reasons, "insufficient_usage")
		}
		// Finalize returns before inspecting independent safety evidence on
		// these rows. Preserve signals that still describe the workload.
		for _, signal := range []struct {
			present bool
			reason  string
		}{
			{row.HPAManaged, "hpa_managed"},
			{row.CurrentPodOOM, "current_pod_oom"},
			{row.WindowOOMEvidence, "oom_in_window"},
			{row.LimitConflict, "limit_conflict"},
			{row.PodLevelRequestValue != nil, rightsizing.ReasonPodLevelRequest},
		} {
			if signal.present {
				r.Status = "review"
				r.Reasons = append(r.Reasons, signal.reason)
			}
		}
		return r
	}
	if row.Observed == nil || row.SampleCount < rightsizing.RightsizingMinSamples {
		return invalid("incomplete_finalized_row")
	}
	if row.Decision.Status == rightsizing.DecisionNone {
		if row.Fit != rightsizing.FitBalanced || baselineOK {
			return invalid("inconsistent_decision")
		}
		if r.EvidenceStatus == "fresh" {
			r.Status = "none"
		}
		return r
	}
	if row.Decision.Status != rightsizing.DecisionActionable || !baselineOK || !targetOK || target <= 0 || baseline <= 0 {
		return invalid("inconsistent_decision")
	}
	switch row.Fit {
	case rightsizing.FitMissingRequest:
		if current != 0 || target != baseline {
			return invalid("invalid_increase")
		}
	case rightsizing.FitUnderRequested:
		if !currentOK || current <= 0 || baseline <= current || target != baseline {
			return invalid("invalid_increase")
		}
	case rightsizing.FitOversized:
		if !currentOK || target > baseline || baseline >= current || target%unit != 0 {
			return invalid("invalid_reduction")
		}
	default:
		return invalid("inconsistent_decision")
	}
	if c.Replicas <= 0 {
		r.Status = "review"
		r.Reasons = append(r.Reasons, "no_positive_replica_count")
		return r
	}
	if r.EvidenceStatus != "fresh" {
		return r
	}
	if c.Evidence.ConfigSince.After(c.At.Add(-7*24*time.Hour)) && !c.Evidence.TargetCurrentConfigVerified {
		r.Reasons = append(r.Reasons, "target_configuration_unverified")
		return r
	}
	if row.Fit != rightsizing.FitOversized {
		r.Status = "increase"
		if row.Fit == rightsizing.FitMissingRequest {
			r.Status = "missing_request"
		}
		r.Next = r.Baseline
		return r
	}
	sparse := *r.RecentCoverage < 0.75
	short := tenths < minDays*10
	if sparse {
		r.Reasons = append(r.Reasons, "sparse_recent_history")
	}
	if short {
		r.Reasons = append(r.Reasons, "insufficient_effective_days")
	}
	if sparse || short {
		return r
	}
	if cpu && row.CurrentLimitValue != nil && *row.CurrentLimitValue > 0 && row.ThrottleRatio == nil {
		r.Status = "review"
		r.Reasons = append(r.Reasons, "throttling_unknown_under_quota")
		return r
	}
	// Inputs fit in 53 bits and credits total 70. Round the intermediate
	// request up once, including when the existing memory request is fractional Mi.
	r.eligibleReduction = true
	numerator := int64(70-tenths)*current + int64(tenths)*target
	divisor := 70 * unit
	next := ((numerator + divisor - 1) / divisor) * unit
	if next >= current {
		r.Status = "none"
		r.Reasons = append(r.Reasons, "no_whole_unit_cut")
		return r
	}
	r.Next = inUnits(next)
	r.VisibleSavings = materialSavings(*r.Current, *r.Next, c.Replicas, row.Resource)
	if r.VisibleSavings == 0 {
		r.Status = "immaterial"
		return r
	}
	r.Status = "cut"
	return r
}
