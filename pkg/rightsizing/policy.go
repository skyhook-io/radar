package rightsizing

import (
	"fmt"
	"math"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	resourcehelper "k8s.io/component-helpers/resource"
)

type RightsizingFit string

const (
	FitBalanced            RightsizingFit = "balanced"
	FitOversized           RightsizingFit = "oversized"
	FitUnderRequested      RightsizingFit = "under_requested"
	FitMissingRequest      RightsizingFit = "missing_request"
	FitInsufficientHistory RightsizingFit = "insufficient_history"
	// FitPodLevelRequest marks a container whose pod sets a fixed pod-level
	// request (spec.resources) for the resource. The scheduler reserves that
	// pod-level value instead of the containers' requests, so no per-container
	// request is suggested.
	FitPodLevelRequest RightsizingFit = "pod_level_request"
)

// ReasonPodLevelRequest is the recommendationReason of a FitPodLevelRequest row.
const ReasonPodLevelRequest = "pod_level_request"

type RightsizingConfidence string

const (
	ConfidenceLow    RightsizingConfidence = "low"
	ConfidenceMedium RightsizingConfidence = "medium"
	ConfidenceHigh   RightsizingConfidence = "high"
)

// OwnerCoverage says how rightsizing established the pods behind a
// recommendation: kube-state-metrics ownership across its seven-day window,
// or only the pods the workload controls now.
type OwnerCoverage string

const (
	OwnerCoverageKSMHistory  OwnerCoverage = "ksm_history"
	OwnerCoverageCurrentPods OwnerCoverage = "current_pods"
)

type ObservedStatistic struct {
	Name      string  `json:"name"`
	Value     float64 `json:"value"`
	Formatted string  `json:"formatted"`
}

type Row struct {
	Decision            Decision              `json:"decision"`
	Container           string                `json:"container"`
	Resource            string                `json:"resource"`
	Fit                 RightsizingFit        `json:"fit"`
	Confidence          RightsizingConfidence `json:"confidence"`
	CurrentRequest      *string               `json:"currentRequest,omitempty"`
	CurrentRequestValue *float64              `json:"currentRequestValue,omitempty"`
	CurrentLimit        *string               `json:"currentLimit,omitempty"`
	CurrentLimitValue   *float64              `json:"currentLimitValue,omitempty"`
	// PodLevelRequest is the request the pod sets in spec.resources for this
	// resource, set whenever the pod has one; the value is in the row's units.
	PodLevelRequest         *string            `json:"podLevelRequest,omitempty"`
	PodLevelRequestValue    *float64           `json:"podLevelRequestValue,omitempty"`
	Observed                *ObservedStatistic `json:"observed,omitempty"`
	Peak                    *ObservedStatistic `json:"peak,omitempty"`
	CalculatedReq           *string            `json:"calculatedRequest,omitempty"`
	CalculatedRequestValue  *float64           `json:"calculatedRequestValue,omitempty"`
	CalculatedAtMinimum     bool               `json:"calculatedAtMinimum,omitempty"`
	RecommendedReq          *string            `json:"recommendedRequest,omitempty"`
	RecommendedRequestValue *float64           `json:"recommendedRequestValue,omitempty"`
	ReductionLimited        bool               `json:"reductionLimited,omitempty"`
	Bursty                  bool               `json:"bursty,omitempty"`
	RecommendationReason    string             `json:"recommendationReason,omitempty"`
	SampleCount             int                `json:"sampleCount"`
	ExpectedSamples         int                `json:"expectedSamples"`
	Coverage                float64            `json:"coverage"`
	HPAManaged              bool               `json:"hpaManaged"`
	HPAEvidenceAvailable    bool               `json:"hpaEvidenceAvailable"`
	ThrottleAvailable       bool               `json:"throttleAvailable,omitempty"`
	ThrottleRatio           *float64           `json:"throttleRatio,omitempty"`
	CurrentPodOOM           bool               `json:"currentPodOOM,omitempty"`
	WindowOOMEvidence       bool               `json:"windowOomEvidence,omitempty"`
	OOMEvidenceAvailable    bool               `json:"oomEvidenceAvailable"`
	// The cluster cache could not answer for this workload's pods — denied, or
	// still warming when the wait ran out — so CurrentPodOOM is false for want
	// of an answer. Unset when the pods were read, including when the workload
	// genuinely has none.
	LiveInventoryUnavailable bool   `json:"liveInventoryUnavailable,omitempty"`
	LimitConflict            bool   `json:"limitConflict,omitempty"`
	QueryError               string `json:"queryError,omitempty"`
}

// ReasonRequestsExceedPodLimit marks a suggested increase that, with the
// workload's other suggested increases, would take the pod's derived request
// above its pod-level limit. The API server rejects such a pod, so the suggestion is
// withheld like one that exceeds the container's own limit.
const ReasonRequestsExceedPodLimit = "requests_exceed_pod_limit"

// withholdIncreasesAbovePodLimit checks a workload's suggested increases
// against the pod-level limit when the pod has one but no fixed pod-level
// request: the pod request is then derived from the containers' requests and
// must stay within the limit. All increases for a resource are applied
// together to a copy of the template, with every other request as it is, so
// they hold whether or not any suggested reduction is applied. The pod request
// is computed the way Kubernetes validates it: container requests default to
// their limits, and init containers and sidecars are counted by resourcehelper.
func withholdIncreasesAbovePodLimit(rows []Row, podSpec *corev1.PodSpec) {
	if podSpec == nil || podSpec.Resources == nil {
		return
	}
	for _, resourceName := range []string{"cpu", "memory"} {
		name := corev1.ResourceName(resourceName)
		limit, limited := podSpec.Resources.Limits[name]
		if !limited || FixedPodLevelRequest(podSpec, name) != nil {
			continue
		}
		increases := map[string]resource.Quantity{}
		for _, row := range rows {
			if row.Resource != resourceName || row.RecommendedReq == nil || row.RecommendedRequestValue == nil {
				continue
			}
			// Kubernetes defaults a missing container request to its limit, so
			// that is the request a suggestion is compared against.
			baseline := row.CurrentRequestValue
			if baseline == nil {
				baseline = row.CurrentLimitValue
			}
			if baseline != nil && *row.RecommendedRequestValue <= *baseline {
				continue
			}
			if q, err := resource.ParseQuantity(*row.RecommendedReq); err == nil {
				increases[row.Container] = q
			}
		}
		if len(increases) == 0 {
			continue
		}
		candidate := &corev1.Pod{Spec: *podSpec.DeepCopy()}
		forEachPodContainer(&candidate.Spec, func(c *corev1.Container) {
			if q, ok := increases[c.Name]; ok {
				if c.Resources.Requests == nil {
					c.Resources.Requests = corev1.ResourceList{}
				}
				c.Resources.Requests[name] = q
			}
			c.Resources.Requests = EffectiveContainerRequests(c.Resources)
		})
		total := resourcehelper.AggregateContainerRequests(candidate, resourcehelper.PodResourcesOptions{})[name]
		if total.Cmp(limit) <= 0 {
			continue
		}
		for i := range rows {
			row := &rows[i]
			if _, increased := increases[row.Container]; !increased || row.Resource != resourceName {
				continue
			}
			row.LimitConflict = true
			row.RecommendationReason = ReasonRequestsExceedPodLimit
			row.RecommendedReq, row.RecommendedRequestValue, row.ReductionLimited = nil, nil, false
		}
	}
}

func forEachPodContainer(spec *corev1.PodSpec, visit func(*corev1.Container)) {
	for i := range spec.Containers {
		visit(&spec.Containers[i])
	}
	for i := range spec.InitContainers {
		visit(&spec.InitContainers[i])
	}
}

// EffectiveContainerRequests includes requests Kubernetes defaults from container limits.
// The returned map is read-only and can share the input's backing storage.
func EffectiveContainerRequests(resources corev1.ResourceRequirements) corev1.ResourceList {
	requests := resources.Requests
	copied := false
	for name, limit := range resources.Limits {
		if _, ok := requests[name]; ok {
			continue
		}
		if !copied {
			requests = resources.Requests.DeepCopy()
			if requests == nil {
				requests = corev1.ResourceList{}
			}
			copied = true
		}
		requests[name] = limit.DeepCopy()
	}
	return requests
}

// FixedPodLevelRequest returns the pod-level request the template sets for a
// resource, or nil when it sets none. With a pod-level request the scheduler
// reserves it and ignores container requests. A pod-level limit alone does not
// fix the request: Kubernetes defaults the request to the limit only when no
// container sets one, so adding container requests changes the reservation
// and sizing them still applies.
func FixedPodLevelRequest(podSpec *corev1.PodSpec, name corev1.ResourceName) *resource.Quantity {
	if podSpec.Resources == nil {
		return nil
	}
	q, ok := podSpec.Resources.Requests[name]
	if !ok {
		return nil
	}
	qc := q.DeepCopy()
	return &qc
}

// MarkPodLevelRequest gives a row whose pod fixes a pod-level request its
// verdict. A row whose usage query failed, or that has no usage samples at all,
// keeps that answer: the evidence gap is still a gap.
func MarkPodLevelRequest(row *Row) {
	if row.PodLevelRequest == nil || row.QueryError != "" || row.Observed == nil {
		return
	}
	row.Fit = FitPodLevelRequest
	row.RecommendationReason = ReasonPodLevelRequest
}

func SetCurrentQuantities(row *Row, req, lim *resource.Quantity, resourceName string) {
	if req != nil {
		value := QuantityToFloat(*req, resourceName)
		formatted := req.String()
		row.CurrentRequest = &formatted
		row.CurrentRequestValue = &value
	}
	if lim != nil {
		value := QuantityToFloat(*lim, resourceName)
		formatted := lim.String()
		row.CurrentLimit = &formatted
		row.CurrentLimitValue = &value
	}
}

func ConfidenceFor(samples int, coverage float64, ownerCoverage OwnerCoverage) RightsizingConfidence {
	if samples < RightsizingMinSamples {
		return ConfidenceLow
	}
	if coverage >= rightsizingHighCoverage && ownerCoverage == OwnerCoverageKSMHistory {
		return ConfidenceHigh
	}
	if coverage >= rightsizingMedCoverage {
		return ConfidenceMedium
	}
	return ConfidenceLow
}

func ClassifyRightsizingFit(row *Row, observed float64, req, lim *resource.Quantity, resourceName string) {
	if row.PodLevelRequest != nil {
		return
	}
	calculated := calculatedRequest(observed, resourceName)
	calculatedValue := QuantityToFloat(resource.MustParse(calculated), resourceName)
	headroomTarget := observed * rightsizingHeadroom
	minimum := rightsizingMinimum(resourceName)
	candidate := max(headroomTarget, minimum)
	if req == nil || QuantityToFloat(*req, resourceName) <= 0 {
		row.Fit = FitMissingRequest
	} else {
		requestValue := QuantityToFloat(*req, resourceName)
		switch {
		case candidate <= requestValue*0.7:
			row.Fit = FitOversized
		case headroomTarget > requestValue:
			row.Fit = FitUnderRequested
		default:
			row.Fit = FitBalanced
		}
	}
	if row.Fit == FitBalanced {
		row.RecommendationReason = "request_within_fit_range"
		return
	}
	unknownThrottleAtLimit := resourceName == "cpu" && lim != nil && lim.Sign() > 0 && row.ThrottleRatio == nil
	conservative := row.Bursty || unknownThrottleAtLimit || (row.ThrottleRatio != nil && *row.ThrottleRatio >= ThrottleReviewRatio)
	recommended, reductionLimited := recommendRequest(observed, req, resourceName, conservative)
	recommendedValue := QuantityToFloat(resource.MustParse(recommended), resourceName)
	row.CalculatedReq = &calculated
	row.CalculatedRequestValue = &calculatedValue
	row.CalculatedAtMinimum = headroomTarget < minimum
	if req != nil {
		requestValue := QuantityToFloat(*req, resourceName)
		if (row.Fit == FitOversized && recommendedValue >= requestValue) ||
			(row.Fit == FitUnderRequested && recommendedValue <= requestValue) {
			row.Fit = FitBalanced
			row.RecommendationReason = "request_within_fit_range"
			return
		}
	}
	if row.HPAManaged {
		row.RecommendationReason = "hpa_managed"
		return
	}
	if row.Fit == FitOversized && !row.HPAEvidenceAvailable {
		row.RecommendationReason = ReasonHPAEvidenceUnavailable
		return
	}
	if resourceName == "memory" && row.Fit == FitOversized && (row.CurrentPodOOM || row.WindowOOMEvidence) {
		row.RecommendationReason = "oom_evidence"
		return
	}
	if resourceName == "memory" && row.Fit == FitOversized && !row.OOMEvidenceAvailable {
		row.RecommendationReason = ReasonOOMEvidenceUnavailable
		return
	}
	if lim != nil && recommendedValue > QuantityToFloat(*lim, resourceName) {
		row.LimitConflict = true
		row.RecommendationReason = "recommended_request_exceeds_limit"
		return
	}
	row.RecommendedReq = &recommended
	row.RecommendedRequestValue = &recommendedValue
	row.ReductionLimited = reductionLimited
}

// DemandTargetBasis names how CalculatedReq was derived, so a reader can tell a
// memory target built from a 7-day max from a CPU P95, or from the floor.
func DemandTargetBasis(row Row) string {
	if row.Observed == nil {
		return ""
	}
	minimum := rightsizingMinimum(row.Resource)
	if row.Observed.Value*rightsizingHeadroom < minimum {
		return "minimum request " + formatRightsizingValue(minimum, row.Resource)
	}
	return fmt.Sprintf("7d %s x %.2f", row.Observed.Name, rightsizingHeadroom)
}

func calculatedRequest(observed float64, resourceName string) string {
	return formatRightsizingValue(max(observed*rightsizingHeadroom, rightsizingMinimum(resourceName)), resourceName)
}

func rightsizingMinimum(resourceName string) float64 {
	if resourceName == "cpu" {
		return rightsizingCPUMin
	}
	return float64(rightsizingMemoryMin)
}

func recommendRequest(observed float64, current *resource.Quantity, resourceName string, conservative bool) (string, bool) {
	calculated := calculatedRequest(observed, resourceName)
	calculatedValue := QuantityToFloat(resource.MustParse(calculated), resourceName)
	if current == nil {
		return calculated, false
	}
	currentValue := QuantityToFloat(*current, resourceName)
	if calculatedValue >= currentValue {
		return calculated, false
	}
	floor := reductionFloor(currentValue, resourceName, conservative)
	if calculatedValue >= floor {
		return calculated, false
	}
	return formatRightsizingValue(floor, resourceName), true
}

func reductionFloor(current float64, resourceName string, conservative bool) float64 {
	if resourceName == "memory" || conservative || current >= 1 {
		return current * 0.5
	}
	if current >= 0.1 {
		return current * 0.25
	}
	return rightsizingCPUMin
}

func IsBurstyCPU(p95, p99 float64) bool {
	return p99-p95 >= 0.05 && p99 >= p95*3
}

// QuantityToFloat converts a K8s Quantity to a float in the same units as
// Prom values (CPU = cores, memory = bytes).
func QuantityToFloat(q resource.Quantity, resKind string) float64 {
	switch resKind {
	case "cpu":
		// MilliValue / 1000 gives cores as float — handles "100m" / "1" / "1.5" uniformly.
		return float64(q.MilliValue()) / 1000.0
	case "memory":
		return float64(q.Value())
	}
	return 0
}

// formatRightsizingValue formats a Prom-shaped value (cores or bytes) into the
// human-friendly form that maps back to spec.resources strings.
func formatRightsizingValue(v float64, resKind string) string {
	switch resKind {
	case "cpu":
		millis := int64(math.Ceil(v * 1000))
		millis = max(millis, 10)
		switch {
		case millis < 100:
			millis = roundUp(millis, 10)
		case millis < 1000:
			millis = roundUp(millis, 50)
		case millis < 4000:
			millis = roundUp(millis, 500)
		default:
			millis = roundUp(millis, 1000)
		}
		if millis < 1000 {
			return fmt.Sprintf("%dm", millis)
		}
		cores := float64(millis) / 1000.0
		if cores == float64(int64(cores)) {
			return fmt.Sprintf("%d", int64(cores))
		}
		return fmt.Sprintf("%.1f", cores)
	case "memory":
		const Mi = 1024 * 1024
		const Gi = 1024 * Mi
		mib := int64(math.Ceil(v / float64(Mi)))
		preferred := []int64{64, 96, 128, 192, 256, 384, 512, 768, 1024, 1536, 2048, 3072, 4096, 6144, 8192}
		for _, candidate := range preferred {
			if mib <= candidate {
				if candidate >= 1024 {
					gib := float64(candidate) / 1024
					if gib == float64(int64(gib)) {
						return fmt.Sprintf("%dGi", int64(gib))
					}
					return fmt.Sprintf("%.1fGi", gib)
				}
				return fmt.Sprintf("%dMi", candidate)
			}
		}
		gib := roundUp(mib, 2*1024) / 1024
		return fmt.Sprintf("%dGi", gib)
	}
	return ""
}

func roundUp(value, step int64) int64 {
	return (value + step - 1) / step * step
}

func FormatObservedValue(v float64, resourceName string) string {
	switch resourceName {
	case "cpu":
		millis := v * 1000
		if millis < 1 {
			return "<1m"
		}
		if millis < 1000 {
			return fmt.Sprintf("%.0fm", millis)
		}
		return fmt.Sprintf("%.2f", v)
	case "memory":
		const Mi = 1024 * 1024
		const Gi = 1024 * Mi
		if v >= float64(Gi) {
			return fmt.Sprintf("%.2fGi", v/float64(Gi))
		}
		return fmt.Sprintf("%.0fMi", v/float64(Mi))
	}
	return ""
}

const (
	rightsizingHeadroom     = 1.15
	rightsizingCPUMin       = 0.01
	rightsizingMemoryMin    = 64 * 1024 * 1024
	RightsizingMinSamples   = 72
	rightsizingHighCoverage = 0.8
	rightsizingMedCoverage  = 0.14
)
const ThrottleReviewRatio = 0.1
const ReasonHPAEvidenceUnavailable = "hpa_evidence_unavailable"
const ReasonOOMEvidenceUnavailable = "oom_evidence_unavailable"

func reviewReasons(row Row) []string {
	var reasons []string
	if row.HPAManaged {
		reasons = append(reasons, "hpa_managed")
	}
	if row.CurrentPodOOM {
		reasons = append(reasons, "current_pod_oom")
	}
	if row.WindowOOMEvidence {
		reasons = append(reasons, "oom_in_window")
	}
	if row.LimitConflict {
		reasons = append(reasons, "limit_conflict")
	}
	if isWithheldRecommendationReason(row.RecommendationReason) {
		reasons = append(reasons, "recommendation_withheld", row.RecommendationReason)
	}
	// Radar sizes container requests, and this pod's reservation is the
	// pod-level request it does not size. Left out of review, the workload
	// would classify in_range: a verdict about a budget nobody evaluated.
	if row.Fit == FitPodLevelRequest {
		reasons = append(reasons, "pod_level_request")
	}
	// Burst and throttling only matter against a cut: they say the smaller
	// request would be met by a workload that already wants more.
	if isReduction(row) {
		if row.Bursty {
			reasons = append(reasons, "bursty_reduction")
		}
		if row.ThrottleRatio != nil && *row.ThrottleRatio >= ThrottleReviewRatio {
			reasons = append(reasons, "throttled_reduction")
		}
	}
	return reasons
}

func isReduction(row Row) bool {
	return row.RecommendedRequestValue != nil && row.CurrentRequestValue != nil &&
		*row.RecommendedRequestValue < *row.CurrentRequestValue
}

const ReasonClusterScopeWithheld = "cluster_scope_withheld"

// isWithheldRecommendationReason keeps the withheld set with the code that
// assigns it: a reason added here without updating every consumer's own copy
// would be reported as a clean result by whichever one was missed.
func isWithheldRecommendationReason(reason string) bool {
	switch reason {
	case ReasonHPAEvidenceUnavailable, ReasonOOMEvidenceUnavailable, ReasonClusterScopeWithheld:
		return true
	}
	return false
}
