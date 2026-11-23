package rightsizing

import corev1 "k8s.io/api/core/v1"

type DecisionStatus string

const (
	DecisionActionable DecisionStatus = "actionable"
	DecisionReview     DecisionStatus = "review"
	DecisionNone       DecisionStatus = "none"
)

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
	WithholdIncreasesAbovePodLimit(rows, spec)
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
			reasons = append(reasons, "qos_change")
		case !scaledToZero && (row.LiveInventoryUnavailable || qos.unknown || !qos.known):
			reasons = append(reasons, "qos_unavailable")
		}
		if !row.HPAEvidenceAvailable {
			reasons = append(reasons, ReasonHPAEvidenceUnavailable)
		}
	}
	if isReduction(row) {
		if row.Confidence != ConfidenceMedium && row.Confidence != ConfidenceHigh {
			reasons = append(reasons, "low_confidence_reduction")
		}
		if row.Resource == "cpu" && row.Peak == nil {
			reasons = append(reasons, "cpu_peak_unavailable")
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
