package rightsizing

import corev1 "k8s.io/api/core/v1"

const (
	reasonQoSUnavailable         = "qos_unavailable"
	reasonCPUPeakUnavailable     = "cpu_peak_unavailable"
	reasonLowConfidenceReduction = "low_confidence_reduction"
)

type DecisionStatus string

const (
	DecisionActionable DecisionStatus = "actionable"
	DecisionReview     DecisionStatus = "review"
	DecisionNone       DecisionStatus = "none"
)

// Decision distinguishes an eligible request change from a proposal requiring review.
// A review row can carry a numeric candidate, but contributes no actionable delta.
type Decision struct {
	Status  DecisionStatus `json:"status"`
	Reasons []string       `json:"reasons,omitempty"`
}

// PodQoS summarizes observed, admitted Pods. The controller template can differ after admission.
type PodQoS struct {
	known      bool
	unknown    bool
	guaranteed bool
}

func (q *PodQoS) Observe(class corev1.PodQOSClass) {
	switch class {
	case corev1.PodQOSGuaranteed:
		q.guaranteed = true
	case corev1.PodQOSBurstable, corev1.PodQOSBestEffort:
		q.known = true
	default:
		q.unknown = true
	}
}

func Finalize(rows []Row, spec *corev1.PodSpec, qos PodQoS, scaledToZero bool) {
	withholdIncreasesAbovePodLimit(rows, spec)
	for i := range rows {
		row := &rows[i]
		row.Decision = decide(*row, qos, scaledToZero)
	}
}

func decide(row Row, qos PodQoS, scaledToZero bool) Decision {
	if row.QueryError != "" || row.Fit == FitInsufficientHistory {
		return Decision{Status: DecisionNone}
	}
	reasons := reviewReasons(row)
	if scaledToZero {
		reasons = append(reasons, "scaled_to_zero")
	}
	if row.RecommendedReq != nil {
		switch {
		case qos.guaranteed:
			reasons = append(reasons, "guaranteed_qos")
		case !scaledToZero && (row.LiveInventoryUnavailable || qos.unknown || !qos.known):
			reasons = append(reasons, reasonQoSUnavailable)
		}
		if !row.HPAEvidenceAvailable {
			reasons = append(reasons, ReasonHPAEvidenceUnavailable)
		}
	}
	if isReduction(row) {
		if row.Confidence != ConfidenceMedium && row.Confidence != ConfidenceHigh {
			reasons = append(reasons, reasonLowConfidenceReduction)
		}
		if row.Resource == "cpu" && row.Peak == nil {
			reasons = append(reasons, reasonCPUPeakUnavailable)
		}
	}
	if len(reasons) > 0 {
		return Decision{Status: DecisionReview, Reasons: reasons}
	}
	if row.RecommendedRequestValue != nil {
		return Decision{Status: DecisionActionable}
	}
	return Decision{Status: DecisionNone}
}

// HasIncompleteEvidence reports whether missing observations withheld a verdict
// or qualified a proposal. Review based on an observed risk alone is complete.
func (r Row) HasIncompleteEvidence() bool {
	if r.QueryError != "" || r.Fit == FitInsufficientHistory || isWithheldRecommendationReason(r.RecommendationReason) {
		return true
	}
	for _, reason := range r.Decision.Reasons {
		switch reason {
		case ReasonHPAEvidenceUnavailable, reasonQoSUnavailable, reasonCPUPeakUnavailable, reasonLowConfidenceReduction:
			return true
		}
	}
	return false
}

func (r Row) RequestChange(replicas int) float64 {
	if r.Decision.Status != DecisionActionable || r.RecommendedRequestValue == nil || replicas <= 0 {
		return 0
	}
	current := 0.0
	if r.CurrentRequestValue != nil {
		current = *r.CurrentRequestValue
	}
	return (*r.RecommendedRequestValue - current) * float64(replicas)
}
